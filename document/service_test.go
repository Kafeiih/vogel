package document_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/storage"
)

// fakeRenderer records the HTML it received and returns pdf or err.
type fakeRenderer struct {
	html []byte
	pdf  []byte
	err  error
}

func (r *fakeRenderer) Render(_ context.Context, html []byte) ([]byte, error) {
	r.html = append([]byte(nil), html...)
	if r.err != nil {
		return nil, r.err
	}
	return r.pdf, nil
}

// fakeStorage keeps uploaded objects in memory and records deletes.
type fakeStorage struct {
	storage.Storage // unused methods panic via nil embed

	mu          sync.Mutex
	objects     map[string][]byte
	contentType map[string]string
	deleted     []string
	uploadErr   error
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string][]byte{}, contentType: map[string]string{}}
}

func (s *fakeStorage) Upload(_ context.Context, in *storage.UploadInput) error {
	if s.uploadErr != nil {
		return s.uploadErr
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[in.Key] = b
	s.contentType[in.Key] = in.ContentType
	return nil
}

func (s *fakeStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, key)
	delete(s.objects, key)
	return nil
}

// fakeRepo is an in-memory document.Repository.
type fakeRepo struct {
	mu        sync.Mutex
	docs      []document.Document
	insertErr error
}

func (r *fakeRepo) Insert(_ context.Context, _ pgxtx.DBTX, d *document.Document) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs = append(r.docs, *d)
	return nil
}

func (r *fakeRepo) GetByID(_ context.Context, _ pgxtx.DBTX, id uuid.UUID) (*document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.docs {
		if d.ID == id {
			return &d, nil
		}
	}
	return nil, document.ErrNotFound
}

func (r *fakeRepo) ListByCase(_ context.Context, _ pgxtx.DBTX, caseID uuid.UUID) ([]document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []document.Document
	for _, d := range r.docs {
		if d.CaseID != nil && *d.CaseID == caseID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *fakeRepo) GetByVerificationKey(_ context.Context, _ pgxtx.DBTX, key string) (*document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.docs {
		if d.VerificationKey == key {
			return &d, nil
		}
	}
	return nil, document.ErrNotFound
}

func (r *fakeRepo) GetBySourceKey(_ context.Context, _ pgxtx.DBTX, key string) (*document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.docs {
		if d.SourceKey == key {
			return &d, nil
		}
	}
	return nil, document.ErrNotFound
}

func (r *fakeRepo) ExistsForCase(_ context.Context, _ pgxtx.DBTX, caseID uuid.UUID, templateName string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.docs {
		if d.CaseID != nil && *d.CaseID == caseID && d.TemplateName == templateName {
			return true, nil
		}
	}
	return false, nil
}

var fixedNow = time.Date(2026, 10, 20, 12, 30, 45, 123456789, time.FixedZone("CLT", -3*3600))

type fixture struct {
	repo     *fakeRepo
	store    *fakeStorage
	renderer *fakeRenderer
	svc      *document.Service
}

func newFixture() *fixture {
	f := &fixture{
		repo:     &fakeRepo{},
		store:    newFakeStorage(),
		renderer: &fakeRenderer{pdf: []byte("%PDF-1.7 fake")},
	}
	f.svc = document.NewService(f.repo, f.store, f.renderer, document.Config{Now: func() time.Time { return fixedNow }}, nil)
	return f
}

func input() document.GenerateInput {
	caseID := uuid.New()
	return document.GenerateInput{
		Domain:     "compras",
		ExternalID: "solicitud-1",
		CaseID:     &caseID,
		Template:   document.Template{Name: "acta", Version: 2, Body: `<p>{{.Name}}</p>`},
		Data:       map[string]any{"Name": "Ana"},
	}
}

func TestGenerate_EscapesTemplateData(t *testing.T) {
	f := newFixture()
	in := input()
	in.Data = map[string]any{"Name": `<script>alert(1)</script>`}

	_, err := f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)

	html := string(f.renderer.html)
	assert.NotContains(t, html, "<script>")
	assert.Contains(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestGenerate_RendererError_NothingUploadedOrStored(t *testing.T) {
	f := newFixture()
	boom := errors.New("gotenberg down")
	f.renderer.err = boom

	_, err := f.svc.Generate(context.Background(), nil, input())
	require.ErrorIs(t, err, boom)
	assert.Empty(t, f.store.objects)
	assert.Empty(t, f.repo.docs)
}

func TestGenerate_EmptyPDF_NothingUploadedOrStored(t *testing.T) {
	f := newFixture()
	f.renderer.pdf = nil

	_, err := f.svc.Generate(context.Background(), nil, input())
	require.ErrorIs(t, err, document.ErrEmptyPDF)
	assert.Empty(t, f.store.objects)
	assert.Empty(t, f.repo.docs)
}

func TestGenerate_UploadError_NoMetadataRow(t *testing.T) {
	f := newFixture()
	boom := errors.New("bucket unavailable")
	f.store.uploadErr = boom

	_, err := f.svc.Generate(context.Background(), nil, input())
	require.ErrorIs(t, err, boom)
	assert.Empty(t, f.repo.docs)
}

func TestGenerate_InsertError_DeletesUploadAndReturnsOriginalError(t *testing.T) {
	f := newFixture()
	boom := errors.New("insert failed")
	f.repo.insertErr = boom

	_, err := f.svc.Generate(context.Background(), nil, input())
	require.ErrorIs(t, err, boom)
	require.Len(t, f.store.deleted, 1)
	assert.Empty(t, f.store.objects, "the uploaded object is removed")
}

func TestGenerate_PersistsMetadataWithHashAndSize(t *testing.T) {
	f := newFixture()
	in := input()

	doc, err := f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)

	sum := sha256.Sum256(f.renderer.pdf)
	assert.NotEqual(t, uuid.Nil, doc.ID)
	assert.Equal(t, hex.EncodeToString(sum[:]), doc.SHA256)
	assert.Equal(t, int64(len(f.renderer.pdf)), doc.Size)
	assert.Equal(t, "compras", doc.Domain)
	assert.Equal(t, "solicitud-1", doc.ExternalID)
	assert.Equal(t, in.CaseID, doc.CaseID)
	assert.Equal(t, "acta", doc.TemplateName)
	assert.Equal(t, 2, doc.TemplateVersion)
	assert.Equal(t, fixedNow.UTC().Truncate(time.Microsecond), doc.CreatedAt)
	assert.Equal(t, time.UTC, doc.CreatedAt.Location())

	wantKey := "documents/compras/" + in.CaseID.String() + "/" + doc.ID.String() + ".pdf"
	assert.Equal(t, wantKey, doc.StorageKey)
	assert.True(t, bytes.Equal(f.renderer.pdf, f.store.objects[wantKey]))
	assert.Equal(t, document.ContentType, f.store.contentType[wantKey])

	require.Len(t, f.repo.docs, 1)
	assert.Equal(t, *doc, f.repo.docs[0])
}

func TestGenerate_WithoutCase_KeyUsesEscapedExternalID(t *testing.T) {
	f := newFixture()
	in := input()
	in.CaseID = nil
	in.ExternalID = "2026/OC 7"

	doc, err := f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)
	assert.Nil(t, doc.CaseID)
	assert.Equal(t, "documents/compras/2026%2FOC%207/"+doc.ID.String()+".pdf", doc.StorageKey)
}

func TestGenerate_VerificationKeyUniqueAndURLSafe(t *testing.T) {
	f := newFixture()

	a, err := f.svc.Generate(context.Background(), nil, input())
	require.NoError(t, err)
	b, err := f.svc.Generate(context.Background(), nil, input())
	require.NoError(t, err)

	assert.NotEmpty(t, a.VerificationKey)
	assert.NotEqual(t, a.VerificationKey, b.VerificationKey)
	assert.GreaterOrEqual(t, len(a.VerificationKey), 43, "at least 256 bits of entropy")
	assert.False(t, strings.ContainsAny(a.VerificationKey, "+/="), "URL-safe without padding")
}

func TestGenerate_InvalidInput_RejectedBeforeRendering(t *testing.T) {
	cases := map[string]func(*document.GenerateInput){
		"empty domain":          func(in *document.GenerateInput) { in.Domain = "" },
		"empty external id":     func(in *document.GenerateInput) { in.ExternalID = "" },
		"empty template name":   func(in *document.GenerateInput) { in.Template.Name = "" },
		"zero template version": func(in *document.GenerateInput) { in.Template.Version = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			in := input()
			mutate(&in)

			_, err := f.svc.Generate(context.Background(), nil, in)
			require.ErrorIs(t, err, document.ErrInvalidInput)
			assert.Nil(t, f.renderer.html, "renderer not called")
		})
	}
}

func TestGenerate_MissingTemplateKey_IsError(t *testing.T) {
	f := newFixture()
	in := input()
	in.Data = map[string]any{}

	_, err := f.svc.Generate(context.Background(), nil, in)
	require.Error(t, err)
	assert.Nil(t, f.renderer.html)
}

func TestLookups_DelegateToRepository(t *testing.T) {
	f := newFixture()
	in := input()
	doc, err := f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)

	got, err := f.svc.Get(context.Background(), nil, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, doc.ID, got.ID)

	list, err := f.svc.ListByCase(context.Background(), nil, *in.CaseID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	byKey, err := f.svc.FindByVerificationKey(context.Background(), nil, doc.VerificationKey)
	require.NoError(t, err)
	assert.Equal(t, doc.ID, byKey.ID)
}

func TestFindByVerificationKey_Empty_IsNotFound(t *testing.T) {
	f := newFixture()
	_, err := f.svc.FindByVerificationKey(context.Background(), nil, "")
	require.ErrorIs(t, err, document.ErrNotFound)
}

func TestGenerate_SameSourceKey_ReturnsExistingWithoutRendering(t *testing.T) {
	f := newFixture()
	in := input()
	in.SourceKey = "case-1/7/acta"

	first, err := f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)
	f.renderer.html = nil

	second, err := f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Nil(t, f.renderer.html, "a known source key must not render again")
	assert.Len(t, f.repo.docs, 1)
	assert.Len(t, f.store.objects, 1)
	assert.Equal(t, "case-1/7/acta", second.SourceKey)
}

func TestGenerate_EmptySourceKey_AlwaysGenerates(t *testing.T) {
	f := newFixture()
	in := input()

	_, err := f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)
	_, err = f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)
	assert.Len(t, f.repo.docs, 2)
}

func TestExistsForCase_MatchesCaseAndTemplateName(t *testing.T) {
	f := newFixture()
	in := input()
	_, err := f.svc.Generate(context.Background(), nil, in)
	require.NoError(t, err)

	ok, err := f.svc.ExistsForCase(context.Background(), nil, *in.CaseID, "acta")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = f.svc.ExistsForCase(context.Background(), nil, *in.CaseID, "orden")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = f.svc.ExistsForCase(context.Background(), nil, uuid.New(), "acta")
	require.NoError(t, err)
	assert.False(t, ok)
}
