package document

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/storage"
)

// Config configures optional Service behaviour.
type Config struct {
	// Now returns the current time. Nil uses time.Now — inject a fixed clock
	// in tests.
	Now func() time.Time
	// KeyPrefix is the storage key prefix. Empty uses DefaultKeyPrefix.
	KeyPrefix string
}

// DefaultKeyPrefix is the storage key prefix used when Config.KeyPrefix is
// empty.
const DefaultKeyPrefix = "documents"

// verificationKeyBytes is the entropy of a verification key: 32 bytes (256
// bits), encoded as 43 characters of unpadded URL-safe base64.
const verificationKeyBytes = 32

// GenerateInput is what Service.Generate needs to produce one document.
type GenerateInput struct {
	Domain     string
	ExternalID string
	// CaseID optionally ties the document to a workflow case.
	CaseID   *uuid.UUID
	Template Template
	// Data is the value the template is executed against.
	Data any
	// SourceKey optionally identifies what asked for the document; see
	// Document.SourceKey.
	SourceKey string
}

func (in GenerateInput) validate() error {
	if in.Domain == "" || in.ExternalID == "" {
		return fmt.Errorf("document: domain and external id are required: %w", ErrInvalidInput)
	}
	// Template.Execute validates the template's name and version.
	return nil
}

// Service generates, stores and looks up documents.
type Service struct {
	repo     Repository
	store    storage.Storage
	renderer Renderer
	logger   *slog.Logger
	now      func() time.Time
	prefix   string
}

// NewService creates a Service. A nil logger falls back to slog.Default().
func NewService(repo Repository, store storage.Storage, renderer Renderer, cfg Config, logger *slog.Logger) *Service {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	prefix := cfg.KeyPrefix
	if prefix == "" {
		prefix = DefaultKeyPrefix
	}
	return &Service{repo: repo, store: store, renderer: renderer, logger: logger, now: now, prefix: prefix}
}

// Generate executes in.Template against in.Data, renders the HTML to PDF,
// uploads it and inserts its metadata row through db, returning the stored
// Document.
//
// The storage key is <prefix>/<domain>/<case id or external id>/<document
// id>.pdf, each segment path-escaped. Every step runs in order and stops at
// the first failure:
//
//   - invalid input or a template error: nothing is rendered;
//   - a Renderer error (or an empty PDF): nothing is uploaded or stored;
//   - an upload error: no metadata row is inserted;
//   - an insert error after a successful upload: Generate attempts a
//     best-effort Delete of the uploaded object and returns the original
//     insert error. A failed Delete is logged, not returned, and leaves an
//     orphan object with no metadata row; nothing references it, so it is
//     safe to sweep later.
//
// When db is a transaction that later rolls back, the metadata row
// disappears but the uploaded object does not: object storage is not
// transactional. Callers that care sweep objects with no metadata row.
func (s *Service) Generate(ctx context.Context, db pgxtx.DBTX, in GenerateInput) (*Document, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if in.SourceKey != "" {
		existing, err := s.repo.GetBySourceKey(ctx, db, in.SourceKey)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("document: look up source key: %w", err)
		}
	}
	html, err := in.Template.Execute(in.Data)
	if err != nil {
		return nil, err
	}
	pdf, err := s.renderer.Render(ctx, html)
	if err != nil {
		return nil, fmt.Errorf("document: render %s@%d: %w", in.Template.Name, in.Template.Version, err)
	}
	if len(pdf) == 0 {
		return nil, ErrEmptyPDF
	}

	verificationKey, err := newVerificationKey()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pdf)
	doc := &Document{
		ID:              uuid.New(),
		Domain:          in.Domain,
		ExternalID:      in.ExternalID,
		CaseID:          in.CaseID,
		TemplateName:    in.Template.Name,
		TemplateVersion: in.Template.Version,
		SHA256:          hex.EncodeToString(sum[:]),
		VerificationKey: verificationKey,
		Size:            int64(len(pdf)),
		SourceKey:       in.SourceKey,
		CreatedAt:       s.now().UTC().Truncate(time.Microsecond),
	}
	doc.StorageKey = s.storageKey(doc)

	if err := s.store.Upload(ctx, &storage.UploadInput{
		Key:         doc.StorageKey,
		Body:        bytes.NewReader(pdf),
		ContentType: ContentType,
	}); err != nil {
		return nil, fmt.Errorf("document: upload %s: %w", doc.StorageKey, err)
	}

	if err := s.repo.Insert(ctx, db, doc); err != nil {
		// Best effort: the row is the only reference to the object, so an
		// object without one is garbage. A cancelled ctx would also cancel
		// the cleanup, hence WithoutCancel.
		if delErr := s.store.Delete(context.WithoutCancel(ctx), doc.StorageKey); delErr != nil {
			s.logger.Error("document: failed to delete uploaded object after insert failure",
				"storage_key", doc.StorageKey, "error", delErr)
		}
		return nil, fmt.Errorf("document: insert metadata: %w", err)
	}
	return doc, nil
}

// ExistsForCase reports whether caseID has at least one document of
// templateName, in any template version.
func (s *Service) ExistsForCase(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, templateName string) (bool, error) {
	return s.repo.ExistsForCase(ctx, db, caseID, templateName)
}

// Get returns the document with id, or an error wrapping ErrNotFound.
func (s *Service) Get(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*Document, error) {
	return s.repo.GetByID(ctx, db, id)
}

// ListByCase returns every document of caseID, oldest first.
func (s *Service) ListByCase(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Document, error) {
	return s.repo.ListByCase(ctx, db, caseID)
}

// FindByVerificationKey returns the document a public verification endpoint
// was asked about. An empty key is ErrNotFound without querying.
func (s *Service) FindByVerificationKey(ctx context.Context, db pgxtx.DBTX, key string) (*Document, error) {
	if key == "" {
		return nil, fmt.Errorf("document: find by verification key: %w", ErrNotFound)
	}
	return s.repo.GetByVerificationKey(ctx, db, key)
}

func (s *Service) storageKey(d *Document) string {
	owner := d.ExternalID
	if d.CaseID != nil {
		owner = d.CaseID.String()
	}
	return s.prefix + "/" + url.PathEscape(d.Domain) + "/" + url.PathEscape(owner) + "/" + d.ID.String() + ".pdf"
}

func newVerificationKey() (string, error) {
	b := make([]byte, verificationKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("document: generate verification key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
