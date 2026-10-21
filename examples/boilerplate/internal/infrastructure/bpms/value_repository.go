package bpms

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"

	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

type ValueRepository struct{}

func NewValueRepository() *ValueRepository { return &ValueRepository{} }

func (ValueRepository) Save(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, nodeID, actorID string, values map[string]any) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		raw, err := json.Marshal(values[k])
		if err != nil {
			return fmt.Errorf("save case value %q: %w", k, err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO case_value (case_id, field, value, node_id, actor_id)
			VALUES ($1, $2, $3, $4, $5)`, caseID, k, raw, nodeID, actorID); err != nil {
			return fmt.Errorf("save case value %q: %w", k, err)
		}
	}
	return nil
}

func (ValueRepository) Current(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) (map[string]any, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT ON (field) field, value
		FROM case_value WHERE case_id = $1
		ORDER BY field, id DESC`, caseID)
	if err != nil {
		return nil, fmt.Errorf("current case values: %w", err)
	}
	defer rows.Close()

	out := map[string]any{}
	for rows.Next() {
		var (
			field string
			raw   []byte
			v     any
		)
		if err := rows.Scan(&field, &raw); err != nil {
			return nil, fmt.Errorf("current case values: %w", err)
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("current case values: %w", err)
		}
		out[field] = v
	}
	return out, rows.Err()
}

func (ValueRepository) History(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]domain.CaseValue, error) {
	rows, err := db.Query(ctx, `
		SELECT field, value, node_id, actor_id, recorded_at
		FROM case_value WHERE case_id = $1 ORDER BY id`, caseID)
	if err != nil {
		return nil, fmt.Errorf("case value history: %w", err)
	}
	defer rows.Close()

	var out []domain.CaseValue
	for rows.Next() {
		var (
			cv  domain.CaseValue
			raw []byte
		)
		if err := rows.Scan(&cv.Field, &raw, &cv.NodeID, &cv.ActorID, &cv.RecordedAt); err != nil {
			return nil, fmt.Errorf("case value history: %w", err)
		}
		if err := json.Unmarshal(raw, &cv.Value); err != nil {
			return nil, fmt.Errorf("case value history: %w", err)
		}
		out = append(out, cv)
	}
	return out, rows.Err()
}
