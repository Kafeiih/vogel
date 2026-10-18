package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/notification"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/storage"
)

// NotifyReviewersArgs is the payload for the notify_reviewers background
// job, enqueued by DocumentHandler.Create in the SAME database transaction
// as the document row, its workflow case, and its audit entry -- see
// worker.Queue.EnqueueTx and the doc comment on DocumentHandler.Create.
type NotifyReviewersArgs struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
}

// Kind implements river.JobArgs.
func (NotifyReviewersArgs) Kind() string { return "notify_reviewers" }

// NotifyReviewersWorker sends a notification when a document enters review.
// It depends only on the notification.Notifier and audit.Repository-backed
// audit.Recorder ports -- never on a concrete adapter such as
// notification/smtp or notification/sendgrid -- so swapping fakeNotifier for
// a real one in main.go's composition root changes nothing here.
type NotifyReviewersWorker struct {
	river.WorkerDefaults[NotifyReviewersArgs]

	notifier notification.Notifier
	recorder *audit.Recorder
	pool     *pgxpool.Pool
	from     string
	logger   *slog.Logger
}

// Work implements river.Worker.
func (w *NotifyReviewersWorker) Work(ctx context.Context, job *river.Job[NotifyReviewersArgs]) error {
	msg := &notification.Message{
		To:      "reviewers@example.com",
		From:    w.from,
		Subject: fmt.Sprintf("Document ready for review: %s", job.Args.Title),
		Text:    fmt.Sprintf("Document %s (%s) is ready for review.", job.Args.Title, job.Args.DocumentID),
	}
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("notify reviewers: invalid message: %w", err)
	}
	if err := w.notifier.Send(ctx, msg); err != nil {
		return fmt.Errorf("notify reviewers: send: %w", err)
	}

	w.logger.InfoContext(ctx, "reviewers notified", "document_id", job.Args.DocumentID)

	// audit.Recorder.Record takes Source as a required positional argument,
	// not an Option, precisely so a call site like this one cannot silently
	// produce an HTTP-shaped audit row for work that never touched a
	// request: audit.SourceWorker here is a compile-time-enforced statement
	// of this entry's true origin, distinct from audit.SourceHTTP used by
	// DocumentHandler. Inside a worker there is no inbound *http.Request and
	// therefore no auth.FromContext principal, so Record's actor fields
	// (ActorID, Username) come out empty here by design -- that reflects
	// reality for a system-initiated notification, not a bug to fix.
	if err := w.recorder.Record(ctx, w.pool, audit.SourceWorker, audit.ActionExecute,
		"document", job.Args.DocumentID,
		audit.WithOperationName("notify_reviewers"),
	); err != nil {
		return fmt.Errorf("notify reviewers: record audit entry: %w", err)
	}

	return nil
}

// ValidateUploadArgs is the payload for the validate_upload background job,
// enqueued by DocumentHandler.AttachFile in the same transaction that marks
// the document's file pending (see worker.Queue.EnqueueTx). Key is the
// upload object the client PUT with its presigned URL. PreviousKey, when
// set, is the final file the document was ready with before this confirm
// detached it: nothing references it any more, so the job deletes it.
type ValidateUploadArgs struct {
	DocumentID  string `json:"document_id"`
	Key         string `json:"key"`
	PreviousKey string `json:"previous_key,omitempty"`
}

// Kind implements river.JobArgs.
func (ValidateUploadArgs) Kind() string { return "validate_upload" }

// ValidateUploadWorker decides whether an object a client uploaded straight
// to storage may become the document's file. It runs here, not in the HTTP
// handler, because the API never sees the bytes of a presigned upload, and
// because inspecting a file (sniffing today; antivirus or conversion in a
// real system) is work that must not hold an HTTP request open.
//
// The verdict comes from the content, never from the type the client
// signed for: storage.SniffContentType reads only the object's first bytes
// and checks them against allowedUploadTypes. The size needs no check here:
// the presigned URL signed the exact Content-Length the client asked for,
// and upload-url refused any size above maxUploadBytes before signing.
//
// An accepted object is never served from its upload key. The upload URL
// stays valid for minutes after the job runs, so whoever holds it could PUT
// new bytes over an object that was already validated. The job therefore
// copies the bytes it inspected to a final key, documents/{id}/file/{uuid},
// which no URL is ever presigned to PUT, and points the document there.
type ValidateUploadWorker struct {
	river.WorkerDefaults[ValidateUploadArgs]

	storage  storage.Storage
	store    *DocumentStore
	recorder *audit.Recorder
	tx       *pgxtx.PgxTxManager
	pool     *pgxpool.Pool
	logger   *slog.Logger
}

// Work implements river.Worker.
//
//   - Object missing (storage.ErrNotFound: the client never uploaded): the
//     file is rejected.
//   - Content not allowed: the object is deleted, THEN the file is rejected,
//     so a failed delete retries the job instead of leaving a rejected row
//     next to a disallowed object nobody will clean up.
//   - Content allowed: the inspected bytes are copied to a new final key
//     (Download then Upload: the port has no Copy), the document moves to
//     that key as ready with the sniffed type -- the only type a download is
//     ever served with -- and the upload object is deleted.
//
// Every verdict is written only while the document still points at Key (see
// DocumentStore.ResolveUpload): a client that confirmed a newer upload in
// the meantime wins, and this stale job changes nothing; a final copy it
// already made is deleted again. Finally PreviousKey, if any, is deleted.
// Any storage or database error is returned, so River retries the job; a
// retry after the row moved on finds a stale key and only cleans up.
func (w *ValidateUploadWorker) Work(ctx context.Context, job *river.Job[ValidateUploadArgs]) error {
	id, err := uuid.Parse(job.Args.DocumentID)
	if err != nil {
		// Retrying cannot fix a malformed payload.
		return river.JobCancel(fmt.Errorf("validate upload: document id: %w", err))
	}
	key := job.Args.Key

	if err := w.decide(ctx, id, key); err != nil {
		return err
	}

	if prev := job.Args.PreviousKey; prev != "" && prev != key {
		if err := w.storage.Delete(ctx, prev); err != nil {
			return fmt.Errorf("validate upload: delete previous file %q: %w", prev, err)
		}
	}
	return nil
}

// decide inspects the upload object at key and records the verdict.
func (w *ValidateUploadWorker) decide(ctx context.Context, id uuid.UUID, key string) error {
	out, err := w.storage.Download(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		w.logger.InfoContext(ctx, "upload missing", "document_id", id, "key", key)
		_, err := w.finish(ctx, id, key, key, FileStatusRejected, "")
		return err
	}
	if err != nil {
		return fmt.Errorf("validate upload: download %q: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()

	contentType, body, err := storage.SniffContentType(out.Body, allowedUploadTypes...)
	if errors.Is(err, storage.ErrContentTypeNotAllowed) {
		w.logger.InfoContext(ctx, "upload content not allowed", "document_id", id, "key", key, "detected", contentType)
		if err := w.storage.Delete(ctx, key); err != nil {
			return fmt.Errorf("validate upload: delete %q: %w", key, err)
		}
		_, err := w.finish(ctx, id, key, key, FileStatusRejected, "")
		return err
	}
	if err != nil {
		return fmt.Errorf("validate upload: sniff %q: %w", key, err)
	}

	// Copy exactly the bytes that were inspected: body replays the sniffed
	// head and then the rest of the same download.
	finalKey := newFileKey(id)
	if err := w.storage.Upload(ctx, &storage.UploadInput{Key: finalKey, Body: body, ContentType: contentType}); err != nil {
		return fmt.Errorf("validate upload: copy %q to %q: %w", key, finalKey, err)
	}

	applied, err := w.finish(ctx, id, key, finalKey, FileStatusReady, contentType)
	if err != nil {
		// The row is unchanged, so the copy is referenced by nothing.
		if delErr := w.storage.Delete(ctx, finalKey); delErr != nil {
			w.logger.ErrorContext(ctx, "delete unused file copy", "key", finalKey, "error", delErr)
		}
		return err
	}
	if !applied {
		if err := w.storage.Delete(ctx, finalKey); err != nil {
			return fmt.Errorf("validate upload: delete unused copy %q: %w", finalKey, err)
		}
	}

	// Ready or stale, nothing reads the upload object any more.
	if err := w.storage.Delete(ctx, key); err != nil {
		return fmt.Errorf("validate upload: delete upload %q: %w", key, err)
	}
	return nil
}

// finish records the verdict on uploadKey for document id -- moving the row
// to storageKey with status and contentType -- with its audit entry, in one
// transaction, and reports whether it did. A document deleted meanwhile, or
// one that no longer points at uploadKey, is left alone and nothing is
// audited.
func (w *ValidateUploadWorker) finish(ctx context.Context, id uuid.UUID, uploadKey, storageKey, status, contentType string) (bool, error) {
	applied := false
	err := w.tx.WithTx(ctx, func(txCtx context.Context) error {
		db := pgxtx.DBFromContext(txCtx, w.pool)

		before, err := w.store.GetByID(txCtx, db, id)
		if errors.Is(err, ErrDocumentNotFound) {
			w.logger.InfoContext(txCtx, "upload validated for a deleted document", "document_id", id, "key", uploadKey)
			return nil
		}
		if err != nil {
			return err
		}

		updated, err := w.store.ResolveUpload(txCtx, db, id, uploadKey, storageKey, status, contentType)
		if err != nil {
			return err
		}
		if !updated {
			w.logger.InfoContext(txCtx, "stale upload validation ignored", "document_id", id, "key", uploadKey)
			return nil
		}

		after := *before
		after.StorageKey = storageKey
		after.FileStatus = status
		after.FileContentType = contentType

		// SourceWorker, not SourceHTTP: this verdict was reached by the job,
		// with no request and no principal behind it (see
		// NotifyReviewersWorker.Work).
		if err := w.recorder.Record(txCtx, db, audit.SourceWorker, audit.ActionUpdate,
			"document", id.String(),
			audit.WithChange(before, &after),
			audit.WithOperationName("document_validate_file"),
		); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("validate upload: record %s verdict: %w", status, err)
	}
	return applied, nil
}

// RegisterWorkers builds the river.Workers registry this example's queue
// runs. Passed as NewRiverQueue's second argument; passing nil instead
// switches a queue to insert-only mode (see the comment in main.go on why
// this example's single process passes a non-nil registry here).
func RegisterWorkers(notify *NotifyReviewersWorker, validate *ValidateUploadWorker) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, notify)
	river.AddWorker(workers, validate)
	return workers
}
