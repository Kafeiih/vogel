package process

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"

	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

type DB interface {
	pgxtx.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

type StoredVersion struct {
	Version int
	Spec    []byte
}

type VersionRepository interface {
	Insert(ctx context.Context, db pgxtx.DBTX, name string, spec []byte) (int, error)
	All(ctx context.Context, db pgxtx.DBTX) ([]StoredVersion, error)
}

type ValueRepository interface {
	Save(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, nodeID, actorID string, values map[string]any) error
	Current(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) (map[string]any, error)
	History(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]domain.CaseValue, error)
}

type CaseReader interface {
	GetByID(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*workflow.Case, error)
}
