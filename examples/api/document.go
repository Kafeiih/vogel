package main

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/kafeiih/vogel/audit"
)

// DocumentDomain identifies this example's workflow domain: the value passed
// as workflow.OpenInput.Domain and workflow.InboxFilter.Domain, so this
// example's cases can be told apart from any other domain a larger
// application built on top of vogel/workflow might register.
const DocumentDomain = "document"

// DocumentKinds lists every valid Document.Kind value accepted by
// DocumentHandler.Create.
var DocumentKinds = []string{"contract", "invoice", "report"}

// File lifecycle values of Document.FileStatus (the documents.file_status
// column, see migrations/002_add_file_status.sql).
const (
	// FileStatusNone: no upload was ever confirmed.
	FileStatusNone = "none"
	// FileStatusPending: the client confirmed an upload and validate_upload
	// has not decided on it yet.
	FileStatusPending = "pending"
	// FileStatusReady: validate_upload accepted the object; only a ready
	// file can be downloaded.
	FileStatusReady = "ready"
	// FileStatusRejected: the object was missing or its content was not an
	// allowed type; a rejected object is deleted.
	FileStatusRejected = "rejected"
)

// Document is the domain entity this example manages. It is deliberately
// plain: no vogel package needs to know about it directly. The only thing
// vogel sees is the audit.Auditable interface implemented below.
type Document struct {
	ID         uuid.UUID
	Title      string
	Kind       string
	IssuedAt   time.Time
	StorageKey string
	// FileStatus is one of the FileStatus* constants.
	FileStatus string
	// FileContentType is the type validate_upload sniffed from the content,
	// "" until a file is ready. Downloads are served with this type, never
	// with one the client declared.
	FileContentType string
	OwnerID         string
	CreatedAt       time.Time
}

// var _ audit.Auditable = (*Document)(nil) documents, at compile time, that
// implementing these two methods is what lets audit.WithSubject and
// audit.WithChange capture a human-readable label and a before/after
// snapshot of a Document automatically, inside audit.Recorder.Record --
// without the audit package ever importing or knowing anything about this
// type.
var _ audit.Auditable = (*Document)(nil)

// AuditRepr implements audit.Auditable.
func (d *Document) AuditRepr() string {
	return fmt.Sprintf("%s (%s)", d.Title, d.Kind)
}

// AuditSnapshot implements audit.Auditable.
func (d *Document) AuditSnapshot() map[string]any {
	return map[string]any{
		"id":                d.ID.String(),
		"title":             d.Title,
		"kind":              d.Kind,
		"issued_at":         d.IssuedAt,
		"storage_key":       d.StorageKey,
		"file_status":       d.FileStatus,
		"file_content_type": d.FileContentType,
		"owner_id":          d.OwnerID,
		"created_at":        d.CreatedAt,
	}
}

// StorageKeyFor is the object key a document's file is stored under. It is
// derived from the document ID rather than stored as a free-form string so
// that the key is reproducible from the row alone, and one document can
// never overwrite another's object.
func StorageKeyFor(id uuid.UUID) string {
	return "documents/" + id.String()
}

// uploadKeyPrefix is the prefix of every object key an upload for the
// document id may use: documents/{id}/uploads/. Keying each upload under its
// own document means a confirm can check, from the key alone, that the
// object belongs to the document it is being attached to.
func uploadKeyPrefix(id uuid.UUID) string {
	return "documents/" + id.String() + "/uploads/"
}
