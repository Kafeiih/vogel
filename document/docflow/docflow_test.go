package docflow_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/document/docflow"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/storage"
	"github.com/kafeiih/vogel/workflow"
)

type memRepo struct {
	mu   sync.Mutex
	docs []document.Document
	err  error
}

func (r *memRepo) Insert(_ context.Context, _ pgxtx.DBTX, d *document.Document) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs = append(r.docs, *d)
	return nil
}

func (r *memRepo) GetByID(context.Context, pgxtx.DBTX, uuid.UUID) (*document.Document, error) {
	return nil, document.ErrNotFound
}

func (r *memRepo) ListByCase(context.Context, pgxtx.DBTX, uuid.UUID) ([]document.Document, error) {
	return nil, nil
}

func (r *memRepo) GetByVerificationKey(context.Context, pgxtx.DBTX, string) (*document.Document, error) {
	return nil, document.ErrNotFound
}

func (r *memRepo) GetBySourceKey(_ context.Context, _ pgxtx.DBTX, key string) (*document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.docs {
		if d.SourceKey == key {
			return &d, nil
		}
	}
	return nil, document.ErrNotFound
}

func (r *memRepo) ExistsForCase(_ context.Context, _ pgxtx.DBTX, caseID uuid.UUID, name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return false, r.err
	}
	for _, d := range r.docs {
		if d.CaseID != nil && *d.CaseID == caseID && d.TemplateName == name {
			return true, nil
		}
	}
	return false, nil
}

type memStore struct {
	storage.Storage
	mu      sync.Mutex
	uploads int
}

func (s *memStore) Upload(context.Context, *storage.UploadInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploads++
	return nil
}

type pdfRenderer struct{}

func (pdfRenderer) Render(context.Context, []byte) ([]byte, error) { return []byte("%PDF fake"), nil }

func newService() (*document.Service, *memRepo, *memStore) {
	repo, store := &memRepo{}, &memStore{}
	return document.NewService(repo, store, pdfRenderer{}, document.Config{}, nil), repo, store
}

var acta = document.Template{Name: "acta", Version: 1, Body: `<p>{{.N}}</p>`}

func TestExists_BlocksUntilTheCaseHasTheDocument(t *testing.T) {
	svc, repo, _ := newService()
	guard := docflow.Exists(svc, "acta")
	c := workflow.Case{ID: uuid.New()}

	ok, err := guard(context.Background(), nil, c)
	require.NoError(t, err)
	assert.False(t, ok)

	caseID := c.ID
	repo.docs = append(repo.docs, document.Document{CaseID: &caseID, TemplateName: "acta"})
	ok, err = guard(context.Background(), nil, c)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestExists_RepositoryErrorPropagates(t *testing.T) {
	svc, repo, _ := newService()
	boom := errors.New("db down")
	repo.err = boom

	_, err := docflow.Exists(svc, "acta")(context.Background(), nil, workflow.Case{ID: uuid.New()})
	require.ErrorIs(t, err, boom)
}

func run() action.Run {
	return action.Run{Effect: "document.generate", CaseID: uuid.New(), Seq: 4, Domain: "compras", ExternalID: "solicitud-9", Attempt: 1}
}

func TestGenerate_FillsCaseReferenceAndSourceKeyFromTheRun(t *testing.T) {
	svc, repo, _ := newService()
	h := docflow.Generate(svc, nil, func(context.Context, action.Run) (document.GenerateInput, error) {
		return document.GenerateInput{Template: acta, Data: map[string]any{"N": 1}}, nil
	})
	r := run()

	require.NoError(t, h.Handle(context.Background(), r))
	require.Len(t, repo.docs, 1)
	d := repo.docs[0]
	require.NotNil(t, d.CaseID)
	assert.Equal(t, r.CaseID, *d.CaseID)
	assert.Equal(t, "compras", d.Domain)
	assert.Equal(t, "solicitud-9", d.ExternalID)
	assert.Equal(t, r.CaseID.String()+"/4/document.generate", d.SourceKey)
}

func TestGenerate_RetriedRunDoesNotDuplicate(t *testing.T) {
	svc, repo, store := newService()
	h := docflow.Generate(svc, nil, func(context.Context, action.Run) (document.GenerateInput, error) {
		return document.GenerateInput{Template: acta, Data: map[string]any{"N": 1}}, nil
	})
	r := run()

	require.NoError(t, h.Handle(context.Background(), r))
	r.Attempt = 2
	require.NoError(t, h.Handle(context.Background(), r))
	assert.Len(t, repo.docs, 1)
	assert.Equal(t, 1, store.uploads)
}

func TestGenerate_InvalidInputCancelsTheJob(t *testing.T) {
	svc, _, _ := newService()
	h := docflow.Generate(svc, nil, func(context.Context, action.Run) (document.GenerateInput, error) {
		return document.GenerateInput{Template: document.Template{}}, nil
	})

	err := h.Handle(context.Background(), run())
	require.ErrorIs(t, err, document.ErrInvalidInput)
	assert.True(t, action.IsCancel(err), "bad input never succeeds on retry")
}

func TestGenerate_BuildErrorIsRetried(t *testing.T) {
	svc, _, _ := newService()
	boom := errors.New("domain store unavailable")
	h := docflow.Generate(svc, nil, func(context.Context, action.Run) (document.GenerateInput, error) {
		return document.GenerateInput{}, boom
	})

	err := h.Handle(context.Background(), run())
	require.ErrorIs(t, err, boom)
	assert.False(t, action.IsCancel(err))
}
