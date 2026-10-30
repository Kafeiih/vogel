package docflow_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
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

func TestGenerate_TemplateFailureCancelsTheJob(t *testing.T) {
	svc, _, _ := newService()
	h := docflow.Generate(svc, nil, func(context.Context, action.Run) (document.GenerateInput, error) {
		return document.GenerateInput{Template: acta, Data: map[string]any{}}, nil
	})

	err := h.Handle(context.Background(), run())
	require.ErrorIs(t, err, document.ErrTemplate)
	assert.True(t, action.IsCancel(err), "a template that cannot execute never succeeds on retry")
}

type fixedRenderer struct {
	pdf []byte
	err error
}

func (f fixedRenderer) Render(context.Context, []byte) ([]byte, error) { return f.pdf, f.err }

func generateWith(t *testing.T, r document.Renderer) error {
	t.Helper()
	svc := document.NewService(&memRepo{}, &memStore{}, r, document.Config{}, nil)
	h := docflow.Generate(svc, nil, func(context.Context, action.Run) (document.GenerateInput, error) {
		return document.GenerateInput{Template: acta, Data: map[string]any{"N": 1}}, nil
	})
	return h.Handle(context.Background(), run())
}

func TestGenerate_RejectedRenderCancelsTheJob(t *testing.T) {
	err := generateWith(t, fixedRenderer{err: fmt.Errorf("renderer: 409: %w", document.ErrRenderRejected)})

	require.ErrorIs(t, err, document.ErrRenderRejected)
	assert.True(t, action.IsCancel(err), "a render the renderer rejects never succeeds on retry")
}

func TestGenerate_EmptyPDFCancelsTheJob(t *testing.T) {
	err := generateWith(t, fixedRenderer{pdf: []byte{}})

	require.ErrorIs(t, err, document.ErrEmptyPDF)
	assert.True(t, action.IsCancel(err), "an empty pdf for the same input never succeeds on retry")
}

func TestGenerate_TransientRenderErrorIsRetried(t *testing.T) {
	boom := errors.New("renderer unavailable")

	err := generateWith(t, fixedRenderer{err: boom})

	require.ErrorIs(t, err, boom)
	assert.False(t, action.IsCancel(err))
}

func TestDocflowGenerate_SourceKeyUnchanged(t *testing.T) {
	svc, repo, _ := newService()
	h := docflow.Generate(svc, nil, func(context.Context, action.Run) (document.GenerateInput, error) {
		return document.GenerateInput{Template: acta, Data: map[string]any{"N": 1}}, nil
	})
	r := run()

	require.NoError(t, h.Handle(context.Background(), r))
	require.Len(t, repo.docs, 1)
	assert.Equal(t, fmt.Sprintf("%s/%d/%s", r.CaseID, r.Seq, r.Effect), repo.docs[0].SourceKey)
	assert.Equal(t, r.IdempotencyKey(), repo.docs[0].SourceKey)
}

// attemptDB captures the attempt row an action.Worker writes, keyed by column.
type attemptDB struct{ row map[string]any }

func (d *attemptDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m := regexp.MustCompile(`(?s)INSERT INTO action_attempt \(([^)]*)\)`).FindStringSubmatch(sql)
	if m == nil {
		return pgconn.CommandTag{}, fmt.Errorf("unexpected statement: %s", sql)
	}
	d.row = map[string]any{}
	for i, col := range strings.Split(m[1], ",") {
		d.row[strings.TrimSpace(col)] = args[i]
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (d *attemptDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not used")
}

func (d *attemptDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func TestDocflowGenerate_RecordsDocumentIDAndSHA(t *testing.T) {
	svc, repo, _ := newService()
	reg := action.NewRegistry(nil)
	require.NoError(t, reg.Register("document.generate", docflow.Generate(svc, nil, func(context.Context, action.Run) (document.GenerateInput, error) {
		return document.GenerateInput{Template: acta, Data: map[string]any{"N": 1}}, nil
	})))
	db := &attemptDB{}
	w := action.NewWorker(reg, action.WithAttemptLog(db), action.WithMetricsRegisterer(prometheus.NewRegistry()))
	job := &river.Job[action.Args]{
		JobRow: &rivertype.JobRow{ID: 1, Attempt: 1, MaxAttempts: 25},
		Args:   action.Args{CaseID: uuid.New(), Seq: 4, Effect: "document.generate", Domain: "compras", ExternalID: "solicitud-9"},
	}

	require.NoError(t, w.Work(context.Background(), job))

	require.Len(t, repo.docs, 1)
	d := repo.docs[0]
	require.NotEmpty(t, d.SHA256)
	assert.Equal(t, d.ID.String(), db.row["result_ref"])
	assert.Equal(t, d.SHA256, db.row["result_digest"])
}
