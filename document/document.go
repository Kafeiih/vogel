// Package document turns a template plus data into a stored PDF with
// metadata: the template is executed with html/template, the resulting HTML
// is converted to PDF by a swappable Renderer, the bytes are uploaded through
// storage.Storage, and a metadata row (case, template, version, sha256,
// storage key, verification key) is persisted through a Repository.
//
// The package deliberately knows nothing about the workflow engine beyond
// plain identifiers: a Document references a (Domain, ExternalID) pair and,
// optionally, a case id, exactly the way a workflow.Case references its
// business object. That keeps it usable outside the engine (a certificate
// issued by a plain HTTP handler, a batch job) and leaves wiring it as a
// workflow effect to a separate package.
//
// Electronic signature is out of scope: only the Signer port is declared.
package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/pgxtx"
)

// ContentType is the MIME type every generated document is stored with.
const ContentType = "application/pdf"

// Template is a named, versioned HTML template. Body is parsed and executed
// with html/template, so every value interpolated from the data is escaped
// for its HTML context automatically: a field containing "<script>" is
// rendered as text, never as markup. Unlike a plain string substitution,
// template data can never inject HTML into the generated document.
type Template struct {
	Name    string
	Version int
	Body    string
}

// Execute parses Body and executes it against data, returning the HTML.
// A reference to a missing map key is an error (missingkey=error), so a
// typo in a template fails loudly instead of rendering an empty value.
func (t Template) Execute(data any) ([]byte, error) {
	if t.Name == "" || t.Version <= 0 {
		return nil, fmt.Errorf("document: template needs a name and a positive version: %w", ErrInvalidInput)
	}
	tmpl, err := template.New(t.Name).Option("missingkey=error").Parse(t.Body)
	if err != nil {
		return nil, fmt.Errorf("document: parse template %s@%d: %w: %w", t.Name, t.Version, ErrTemplate, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("document: execute template %s@%d: %w: %w", t.Name, t.Version, ErrTemplate, err)
	}
	return buf.Bytes(), nil
}

// Renderer converts an HTML document to PDF bytes. Implementations must be
// safe for concurrent use, and wrap ErrRenderRejected in errors a retry
// cannot fix. The first adapter is document/gotenberg.
type Renderer interface {
	Render(ctx context.Context, html []byte) ([]byte, error)
}

// Signer applies an electronic signature to PDF bytes and returns the
// signed PDF. Only the port is declared: signing is out of scope for this
// package and no implementation is provided. A consumer that needs signed
// documents supplies its own Signer and applies it around a Renderer.
type Signer interface {
	Sign(ctx context.Context, pdf []byte) ([]byte, error)
}

// Document is the metadata record of one generated PDF. The bytes
// themselves live in object storage under StorageKey.
type Document struct {
	ID         uuid.UUID
	Domain     string
	ExternalID string
	// CaseID is the workflow case the document belongs to, when any. It is
	// a plain identifier: this package never reads the case.
	CaseID          *uuid.UUID
	TemplateName    string
	TemplateVersion int
	// SHA256 is the lowercase hex digest of the stored PDF bytes.
	SHA256     string
	StorageKey string
	// VerificationKey is a random, URL-safe token (crypto/rand) a public
	// verification endpoint can look the document up by, without exposing
	// the internal ID or the storage key.
	VerificationKey string
	Size            int64
	// SourceKey identifies what produced the document (for example one
	// workflow effect run). Empty means none. Generate returns the existing
	// document for a known non-empty SourceKey instead of generating again,
	// so a retried job never creates a duplicate.
	SourceKey string
	CreatedAt time.Time
}

// Repository persists Document metadata. Every method receives the
// pgxtx.DBTX to run against, so the caller decides whether it runs inside
// its own transaction.
type Repository interface {
	// Insert persists d. A duplicate id, storage key or verification key is
	// an error.
	Insert(ctx context.Context, db pgxtx.DBTX, d *Document) error
	// GetByID returns the document with id, or an error wrapping
	// ErrNotFound.
	GetByID(ctx context.Context, db pgxtx.DBTX, id uuid.UUID) (*Document, error)
	// ListByCase returns every document of caseID ordered by creation time
	// (oldest first); an empty result is not an error.
	ListByCase(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Document, error)
	// GetByVerificationKey returns the document whose verification key is
	// key, or an error wrapping ErrNotFound.
	GetByVerificationKey(ctx context.Context, db pgxtx.DBTX, key string) (*Document, error)
	// GetBySourceKey returns the document whose source key is key, or an
	// error wrapping ErrNotFound.
	GetBySourceKey(ctx context.Context, db pgxtx.DBTX, key string) (*Document, error)
	// ExistsForCase reports whether caseID has at least one document of
	// templateName, in any template version.
	ExistsForCase(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, templateName string) (bool, error)
}

// Sentinel errors callers can branch on via errors.Is.
var (
	ErrNotFound     = errors.New("document: not found")
	ErrInvalidInput = errors.New("document: invalid input")
	// ErrTemplate reports that a template failed to parse or execute (for
	// example a key missing from the data). Retrying the same input cannot
	// fix it.
	ErrTemplate = errors.New("document: template failed")
	// ErrEmptyPDF reports that the Renderer returned no bytes; nothing is
	// uploaded or stored.
	ErrEmptyPDF = errors.New("document: renderer returned an empty pdf")
	// ErrRenderRejected is wrapped by a Renderer error that retrying the
	// same HTML cannot fix (the renderer refused or could not convert it),
	// as opposed to an outage or a timeout worth retrying.
	ErrRenderRejected = errors.New("document: renderer rejected the document")
)
