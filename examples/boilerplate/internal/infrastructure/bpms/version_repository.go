package bpms

import (
	"context"
	"fmt"

	"github.com/kafeiih/vogel/pgxtx"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/process"
)

type VersionRepository struct{}

func NewVersionRepository() *VersionRepository { return &VersionRepository{} }

// Two concurrent publishes of one name collide on the primary key; one fails.
func (VersionRepository) Insert(ctx context.Context, db pgxtx.DBTX, name string, spec []byte) (int, error) {
	var version int
	err := db.QueryRow(ctx, `
		INSERT INTO process_version (name, version, spec)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2
		FROM process_version WHERE name = $1
		RETURNING version`, name, spec).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("insert process version: %w", err)
	}
	return version, nil
}

func (VersionRepository) All(ctx context.Context, db pgxtx.DBTX) ([]process.StoredVersion, error) {
	rows, err := db.Query(ctx, `SELECT version, spec FROM process_version ORDER BY name, version`)
	if err != nil {
		return nil, fmt.Errorf("list process versions: %w", err)
	}
	defer rows.Close()

	var out []process.StoredVersion
	for rows.Next() {
		var sv process.StoredVersion
		if err := rows.Scan(&sv.Version, &sv.Spec); err != nil {
			return nil, fmt.Errorf("list process versions: %w", err)
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}
