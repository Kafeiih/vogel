package http

import (
	"context"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/workflow"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/process"
	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

type matrixProcessService struct{}

func (matrixProcessService) Publish(context.Context, domain.ProcessSpec) (*domain.Process, error) {
	return nil, domain.ErrProcessNotFound
}

func (matrixProcessService) Latest(string) (*domain.Process, bool) { return nil, false }

func (matrixProcessService) Start(context.Context, string, string, string, map[string]any) (*workflow.Case, error) {
	return nil, domain.ErrProcessNotFound
}

func (matrixProcessService) Inbox(context.Context, string) ([]workflow.Case, error) { return nil, nil }

func (matrixProcessService) View(context.Context, string, uuid.UUID) (*process.CaseView, error) {
	return nil, workflow.ErrCaseNotFound
}

func (matrixProcessService) Claim(context.Context, string, uuid.UUID) (*workflow.Case, error) {
	return nil, workflow.ErrCaseNotFound
}

func (matrixProcessService) Submit(context.Context, string, uuid.UUID, string, map[string]any) (*workflow.Case, error) {
	return nil, workflow.ErrCaseNotFound
}
