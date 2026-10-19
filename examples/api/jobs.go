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
	"github.com/kafeiih/vogel/worker"
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
// upload object the client PUT with its presigned URL. The file the upload
// would replace is not in the payload: it is on the row
// (Document.PreviousStorageKey), where downloads also find it.
type ValidateUploadArgs struct {
	DocumentID string `json:"document_id"`
	Key        string `json:"key"`
}

// Kind implements river.JobArgs.
func (ValidateUploadArgs) Kind() string { return "validate_upload" }

// validateUploadMaxAttempts bounds how often River runs validate_upload
// before the last attempt resolves the document (see giveUp). River's
// default of 25 attempts spreads retries over weeks, during which the
// document stays pending. With 5, River's backoff (attempt^4 seconds,
// +/-10% jitter: about 1s, 16s, 81s and 256s between attempts) plus up to
// 5 x the 1-minute default job timeout puts the last attempt within about
// 12 minutes of the confirm.
const validateUploadMaxAttempts = 5

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
	// queue enqueues delete_object for the file an acceptance retires, in
	// the acceptance's own transaction (see finish).
	queue  worker.Queue
	tx     *pgxtx.PgxTxManager
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// Work implements river.Worker.
//
//   - Object missing (storage.ErrNotFound: the client never uploaded, or a
//     retried confirm named an upload an earlier job already consumed): the
//     upload is rejected.
//   - Content not allowed: the object is deleted, THEN the upload is
//     rejected, so a failed delete retries the job instead of leaving a
//     disallowed object nobody will clean up.
//   - Content allowed: the inspected bytes are copied to a new final key
//     (Download then Upload: the port has no Copy), and the document moves
//     to that key as ready with the sniffed type -- the only type a download
//     is ever served with. The file it replaces, if any, is retired: its
//     deletion is enqueued as a delete_object job in the same transaction.
//
// A rejected upload never costs the document the file it already had: when
// the row holds a previous file, it goes back to it, ready, with its type
// (audited as document_restore_file); only without one does it end
// rejected.
//
// Every verdict is written only while the document still points at Key with
// its file pending (see DocumentStore.ResolveUpload): a retry of a job that
// already resolved the row changes nothing, enqueues nothing and audits
// nothing, and a final copy such a run made is deleted again. The verdict
// commits before the upload object is deleted (see cleanup), so a failed
// delete returns an error and the retry, finding the row resolved, finishes
// it. Any storage or database error is returned, so River retries the job.
//
// On the last attempt River would discard a failing job and leave the
// document pending, so the job resolves it by the rules of a rejection (see
// giveUp) and still returns the error for River to record.
func (w *ValidateUploadWorker) Work(ctx context.Context, job *river.Job[ValidateUploadArgs]) error {
	id, err := uuid.Parse(job.Args.DocumentID)
	if err != nil {
		// Retrying cannot fix a malformed payload.
		return river.JobCancel(fmt.Errorf("validate upload: document id: %w", err))
	}
	err = w.decide(ctx, id, job.Args)
	if err == nil || job.JobRow == nil || job.Attempt < job.MaxAttempts {
		return err
	}
	return w.giveUp(ctx, id, job.Args, job.Attempt, err)
}

// giveUp ends a job that failed on its last attempt: it logs the failure
// (document and key, never a URL), deletes the upload object best effort,
// resolves the row like a rejection -- back to the previous ready file if
// there is one, rejected otherwise -- with one audit entry, and runs the
// cleanup best effort. It returns cause, joined with any error resolving
// the row.
func (w *ValidateUploadWorker) giveUp(ctx context.Context, id uuid.UUID, args ValidateUploadArgs, attempt int, cause error) error {
	w.logger.ErrorContext(ctx, "upload validation failed on its last attempt; resolving as rejected",
		"document_id", id, "key", args.Key, "attempt", attempt, "error", cause)
	if err := w.storage.Delete(ctx, args.Key); err != nil {
		w.logger.WarnContext(ctx, "delete upload after the last attempt", "document_id", id, "key", args.Key, "error", err)
	}
	if _, err := w.finish(ctx, id, args.Key, rejectVerdict); err != nil {
		return errors.Join(cause, err)
	}
	if err := w.cleanup(ctx, id, args); err != nil {
		w.logger.WarnContext(ctx, "cleanup after the last attempt", "document_id", id, "key", args.Key, "error", err)
	}
	return cause
}

// decide inspects the upload object at args.Key, records the verdict and
// cleans up.
func (w *ValidateUploadWorker) decide(ctx context.Context, id uuid.UUID, args ValidateUploadArgs) error {
	key := args.Key
	out, err := w.storage.Download(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		w.logger.InfoContext(ctx, "upload missing", "document_id", id, "key", key)
		if _, err := w.finish(ctx, id, key, rejectVerdict); err != nil {
			return err
		}
		return w.cleanup(ctx, id, args)
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
		if _, err := w.finish(ctx, id, key, rejectVerdict); err != nil {
			return err
		}
		return w.cleanup(ctx, id, args)
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

	applied, err := w.finish(ctx, id, key, acceptVerdict(finalKey, contentType))
	if err != nil {
		// The error may have come after the commit landed: keep the copy if
		// the row points at it; the retry sorts out the rest.
		w.deleteCopyUnlessReferenced(ctx, id, finalKey)
		return err
	}
	if !applied {
		if err := w.storage.Delete(ctx, finalKey); err != nil {
			return fmt.Errorf("validate upload: delete unused copy %q: %w", finalKey, err)
		}
	}
	return w.cleanup(ctx, id, args)
}

// cleanup deletes the upload object once the row no longer waits on it. It
// re-reads the row on every run, so a retry after a failed delete finishes
// the job (Delete is idempotent), and it decides from the row rather than
// from what this run did. The file an acceptance retires is not deleted
// here: by then the row no longer names it, so its key travels in the
// delete_object job the acceptance enqueued instead.
func (w *ValidateUploadWorker) cleanup(ctx context.Context, id uuid.UUID, args ValidateUploadArgs) error {
	doc, err := w.store.GetByID(ctx, w.pool, id)
	if errors.Is(err, ErrDocumentNotFound) {
		doc = nil
	} else if err != nil {
		return fmt.Errorf("validate upload: re-read document: %w", err)
	}
	if doc != nil && doc.FileStatus == FileStatusPending && doc.StorageKey == args.Key {
		return nil
	}
	if err := w.storage.Delete(ctx, args.Key); err != nil {
		return fmt.Errorf("validate upload: delete upload %q: %w", args.Key, err)
	}
	return nil
}

// deleteCopyUnlessReferenced deletes the final copy key after a failed
// finish, unless the row references it (the commit landed before the error)
// or the row cannot be read (keeping an orphan beats deleting a served
// file). Failures are logged, not returned: the caller is already returning
// finish's error.
func (w *ValidateUploadWorker) deleteCopyUnlessReferenced(ctx context.Context, id uuid.UUID, key string) {
	doc, err := w.store.GetByID(ctx, w.pool, id)
	switch {
	case errors.Is(err, ErrDocumentNotFound):
	case err != nil:
		w.logger.ErrorContext(ctx, "keep file copy: cannot re-read document", "document_id", id, "key", key, "error", err)
		return
	case doc.StorageKey == key:
		return
	}
	if err := w.storage.Delete(ctx, key); err != nil {
		w.logger.ErrorContext(ctx, "delete unused file copy", "document_id", id, "key", key, "error", err)
	}
}

// verdict is what finish writes: the row's new key, status and type, the
// audit operation that records it, a label for errors, and the file it
// retires (enqueued for deletion with the verdict). It is computed from the
// row as finish reads it inside its transaction, so a restore always uses
// the previous file the row holds at that moment.
type verdict struct {
	storageKey  string
	status      string
	contentType string
	operation   string
	label       string
	retire      string
}

// verdictFor builds a verdict from the row before it changes.
type verdictFor func(before *Document) verdict

// acceptVerdict moves the row to the validated copy at finalKey and retires
// the previous file, if the row holds one.
func acceptVerdict(finalKey, contentType string) verdictFor {
	return func(before *Document) verdict {
		return verdict{
			storageKey: finalKey, status: FileStatusReady, contentType: contentType,
			operation: "document_validate_file", label: "acceptance",
			retire: before.PreviousStorageKey,
		}
	}
}

// rejectVerdict puts the row back on its previous ready file when it holds
// one (document_restore_file), and marks the upload rejected otherwise.
func rejectVerdict(before *Document) verdict {
	if before.PreviousStorageKey != "" {
		return verdict{
			storageKey: before.PreviousStorageKey, status: FileStatusReady, contentType: before.PreviousContentType,
			operation: "document_restore_file", label: "restore of the previous file",
		}
	}
	return verdict{
		storageKey: before.StorageKey, status: FileStatusRejected,
		operation: "document_validate_file", label: "rejection",
	}
}

// finish records the verdict buildVerdict builds for uploadKey on document id, in
// one transaction with its audit entry and, when the verdict retires a
// file, the delete_object job for it; it reports whether it did. A document
// deleted meanwhile, or one that is no longer pending on uploadKey, is left
// alone: nothing is written, enqueued or audited.
func (w *ValidateUploadWorker) finish(ctx context.Context, id uuid.UUID, uploadKey string, buildVerdict verdictFor) (bool, error) {
	applied := false
	label := "verdict"
	err := w.tx.WithTx(ctx, func(txCtx context.Context) error {
		db := pgxtx.DBFromContext(txCtx, w.pool)

		before, err := w.store.GetByIDForUpdate(txCtx, db, id)
		if errors.Is(err, ErrDocumentNotFound) {
			w.logger.InfoContext(txCtx, "upload validated for a deleted document", "document_id", id, "key", uploadKey)
			return nil
		}
		if err != nil {
			return err
		}
		v := buildVerdict(before)
		label = v.label

		updated, err := w.store.ResolveUpload(txCtx, db, id, uploadKey, v.storageKey, v.status, v.contentType)
		if err != nil {
			return err
		}
		if !updated {
			w.logger.InfoContext(txCtx, "upload already resolved; verdict ignored", "document_id", id, "key", uploadKey)
			return nil
		}

		// The retired file's key leaves the row with this commit, so it is
		// handed to its own job in the same transaction: River keeps it,
		// and retries its deletion, whatever happens to this one.
		if v.retire != "" && v.retire != v.storageKey {
			if _, err := w.queue.EnqueueTx(txCtx, pgxtx.TxFromContext(txCtx), DeleteObjectArgs{Key: v.retire}, nil); err != nil {
				return err
			}
		}

		after := *before
		after.StorageKey = v.storageKey
		after.FileStatus = v.status
		after.FileContentType = v.contentType
		after.FilePendingSince = nil
		after.PreviousStorageKey = ""
		after.PreviousContentType = ""

		// SourceWorker, not SourceHTTP: this verdict was reached by the job,
		// with no request and no principal behind it (see
		// NotifyReviewersWorker.Work).
		if err := w.recorder.Record(txCtx, db, audit.SourceWorker, audit.ActionUpdate,
			"document", id.String(),
			audit.WithChange(before, &after),
			audit.WithOperationName(v.operation),
		); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("validate upload: record %s: %w", label, err)
	}
	return applied, nil
}

// DeleteObjectArgs is the payload for delete_object: one storage object
// nothing references any more. validate_upload enqueues it, in the
// transaction that stops the document referencing the object, for the file
// an accepted upload replaces.
type DeleteObjectArgs struct {
	Key string `json:"key"`
}

// Kind implements river.JobArgs.
func (DeleteObjectArgs) Kind() string { return "delete_object" }

// DeleteObjectWorker deletes one storage object. Delete is idempotent, so a
// retry after a partial failure is safe; any error is returned for River
// to retry. Nothing is audited: the change the deletion follows from was.
type DeleteObjectWorker struct {
	river.WorkerDefaults[DeleteObjectArgs]

	storage storage.Storage
	logger  *slog.Logger
}

// Work implements river.Worker.
func (w *DeleteObjectWorker) Work(ctx context.Context, job *river.Job[DeleteObjectArgs]) error {
	if err := w.storage.Delete(ctx, job.Args.Key); err != nil {
		return fmt.Errorf("delete object %q: %w", job.Args.Key, err)
	}
	w.logger.InfoContext(ctx, "retired object deleted", "key", job.Args.Key)
	return nil
}

// RegisterWorkers builds the river.Workers registry this example's queue
// runs. Passed as NewRiverQueue's second argument; passing nil instead
// switches a queue to insert-only mode (see the comment in main.go on why
// this example's single process passes a non-nil registry here).
func RegisterWorkers(notify *NotifyReviewersWorker, validate *ValidateUploadWorker, deleteObject *DeleteObjectWorker) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, notify)
	river.AddWorker(workers, validate)
	river.AddWorker(workers, deleteObject)
	return workers
}
