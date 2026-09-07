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

// Document is the domain entity this example manages. It is deliberately
// plain: no vogel package needs to know about it directly. The only thing
// vogel sees is the audit.Auditable interface implemented below.
type Document struct {
	ID         uuid.UUID
	Title      string
	Kind       string
	IssuedAt   time.Time
	StorageKey string
	OwnerID    string
	CreatedAt  time.Time
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
		"id":          d.ID.String(),
		"title":       d.Title,
		"kind":        d.Kind,
		"issued_at":   d.IssuedAt,
		"storage_key": d.StorageKey,
		"owner_id":    d.OwnerID,
		"created_at":  d.CreatedAt,
	}
}

// StorageKeyFor is the object key a document's file is stored under. It is
// derived from the document ID rather than stored as a free-form string so
// that the key is reproducible from the row alone, and one document can
// never overwrite another's object.
func StorageKeyFor(id uuid.UUID) string {
	return "documents/" + id.String()
}
