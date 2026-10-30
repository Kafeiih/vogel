// Package docflow connects document to the workflow engine and the action
// runner: Generate is the handler behind a "document.generate" effect, and
// Exists is the guard that keeps a case from advancing until a document of a
// given template exists for it. The document package itself stays
// independent of both.
package docflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

// Exists returns a guard that passes only when the case already has a
// document of templateName, in any template version. Register it with
// Engine.RegisterGuard and name it on the transition that needs the
// document. Generation runs in the background, so a step that depends on a
// document must use this guard instead of assuming it is there.
func Exists(svc *document.Service, templateName string) workflow.GuardFunc {
	return func(ctx context.Context, db pgxtx.DBTX, c workflow.Case) (bool, error) {
		return svc.ExistsForCase(ctx, db, c.ID, templateName)
	}
}

// BuildFunc returns the template and data for the document a run generates.
// It may leave Domain, ExternalID, CaseID and SourceKey empty: Generate fills
// them from the run.
type BuildFunc func(ctx context.Context, r action.Run) (document.GenerateInput, error)

// Generate returns the handler for a document-generating effect. Each run
// generates at most one document: the source key defaults to
// r.IdempotencyKey() ("<case id>/<event seq>/<effect>"), so a retried job returns the document a
// previous attempt stored. Input the document package rejects as invalid, a
// template that fails to parse or execute, a document the Renderer rejects
// (document.ErrRenderRejected) and an empty PDF cancel the job, since a
// retry cannot fix them; every other error, including one from build, is
// returned so River retries. db is where the metadata row
// is written, typically the pool. The document's id and sha256 are recorded
// on the attempt (action.RecordResult), so a Worker with an attempt log ties
// each attempt to the document it produced or found.
func Generate(svc *document.Service, db pgxtx.DBTX, build BuildFunc) action.Handler {
	return action.HandlerFunc(func(ctx context.Context, r action.Run) error {
		in, err := build(ctx, r)
		if err != nil {
			return fmt.Errorf("docflow: build input for %s: %w", r.Effect, err)
		}
		if in.Domain == "" {
			in.Domain = r.Domain
		}
		if in.ExternalID == "" {
			in.ExternalID = r.ExternalID
		}
		if in.CaseID == nil {
			caseID := r.CaseID
			in.CaseID = &caseID
		}
		if in.SourceKey == "" {
			in.SourceKey = r.IdempotencyKey()
		}
		doc, err := svc.Generate(ctx, db, in)
		if err != nil {
			if permanent(err) {
				return river.JobCancel(err)
			}
			return err
		}
		action.RecordResult(ctx, action.Result{Ref: doc.ID.String(), Digest: doc.SHA256})
		return nil
	})
}

func permanent(err error) bool {
	for _, target := range []error{document.ErrInvalidInput, document.ErrTemplate, document.ErrRenderRejected, document.ErrEmptyPDF} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
