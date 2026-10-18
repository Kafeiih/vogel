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
	// FilePendingSince is when the file last went pending, nil otherwise.
	FilePendingSince *time.Time
	// PreviousStorageKey and PreviousContentType are the ready file a
	// pending replacement would supersede ("" otherwise): DownloadURL keeps
	// serving it while the replacement is validated, and validate_upload
	// restores it if the replacement is rejected.
	PreviousStorageKey  string
	PreviousContentType string
	OwnerID             string
	CreatedAt           time.Time
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
		"id":                   d.ID.String(),
		"title":                d.Title,
		"kind":                 d.Kind,
		"issued_at":            d.IssuedAt,
		"storage_key":          d.StorageKey,
		"file_status":          d.FileStatus,
		"file_content_type":    d.FileContentType,
		"previous_storage_key": d.PreviousStorageKey,
		"owner_id":             d.OwnerID,
		"created_at":           d.CreatedAt,
	}
}

// uploadKeyPrefix is the prefix of every object key an upload for the
// document id may use: documents/{id}/uploads/. Keying each upload under its
// own document means a confirm can check, from the key alone, that the
// object belongs to the document it is being attached to.
func uploadKeyPrefix(id uuid.UUID) string {
	return "documents/" + id.String() + "/uploads/"
}

// fileKeyPrefix is the prefix of the final key a validated file lives
// under: documents/{id}/file/. Only validate_upload writes there, by copying
// an accepted upload; no URL is ever presigned to PUT under it, and
// AttachFile accepts only upload keys, so a client can never point a
// document at, or overwrite, a validated file.
func fileKeyPrefix(id uuid.UUID) string {
	return "documents/" + id.String() + "/file/"
}

// newFileKey is a fresh final key for one validated file of the document id.
func newFileKey(id uuid.UUID) string {
	return fileKeyPrefix(id) + uuid.NewString()
}

// newUploadKey is a fresh object key for one upload to the document id:
// uploadKeyPrefix(id) followed by a random UUID. A new key per upload means
// a replacement never overwrites the object a previous upload (or a
// validation job still reading it) is using.
func newUploadKey(id uuid.UUID) string {
	return uploadKeyPrefix(id) + uuid.NewString()
}
