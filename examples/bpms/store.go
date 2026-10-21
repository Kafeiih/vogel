package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

//go:embed migrations/*.sql
var appMigrations embed.FS

// appMigrationsFS returns this example's migrations rooted at their
// directory; goose only scans the root of the fs.FS it is handed (see the
// same helper in examples/api).
func appMigrationsFS() (fs.FS, error) {
	return fs.Sub(appMigrations, "migrations")
}

// CaseValue is one recorded value of a case field: which node collected it,
// who, and when. Values are only ever appended; the current value of a
// field is its latest record.
type CaseValue struct {
	Field      string    `json:"field"`
	Value      any       `json:"value"`
	NodeID     string    `json:"nodeId"`
	ActorID    string    `json:"actorId"`
	RecordedAt time.Time `json:"recordedAt"`
}

// SaveCaseValues appends values collected on nodeID by actorID. Keys are
// written in sorted order so the history reads deterministically.
func SaveCaseValues(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, nodeID, actorID string, values map[string]any) error {
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

// CurrentCaseValues returns the latest value of every field of the case.
func CurrentCaseValues(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) (map[string]any, error) {
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

// CaseValueHistory returns every recorded value of the case, oldest first.
func CaseValueHistory(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]CaseValue, error) {
	rows, err := db.Query(ctx, `
		SELECT field, value, node_id, actor_id, recorded_at
		FROM case_value WHERE case_id = $1 ORDER BY id`, caseID)
	if err != nil {
		return nil, fmt.Errorf("case value history: %w", err)
	}
	defer rows.Close()

	var out []CaseValue
	for rows.Next() {
		var (
			cv  CaseValue
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
