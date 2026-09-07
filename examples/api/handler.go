package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/request"
	"github.com/kafeiih/vogel/storage"
	"github.com/kafeiih/vogel/stringutil"
	"github.com/kafeiih/vogel/worker"
	"github.com/kafeiih/vogel/workflow"
)

// DocumentHandler wires every vogel port this example needs to serve
// documents over HTTP: persistence (store), the workflow engine (engine),
// the audit trail (recorder), background jobs (queue), and file storage
// (storage).
type DocumentHandler struct {
	store    *DocumentStore
	engine   *workflow.Engine
	recorder *audit.Recorder
	queue    worker.Queue
	storage  storage.Storage
	tx       *pgxtx.PgxTxManager
	pool     *pgxpool.Pool
	logger   *slog.Logger
}

// NewDocumentHandler builds a DocumentHandler from its dependencies.
func NewDocumentHandler(
	store *DocumentStore,
	engine *workflow.Engine,
	recorder *audit.Recorder,
	queue worker.Queue,
	fileStorage storage.Storage,
	tx *pgxtx.PgxTxManager,
	pool *pgxpool.Pool,
	logger *slog.Logger,
) *DocumentHandler {
	return &DocumentHandler{
		store:    store,
		engine:   engine,
		recorder: recorder,
		queue:    queue,
		storage:  fileStorage,
		tx:       tx,
		pool:     pool,
		logger:   logger,
	}
}

// actorID reads the acting user's ID from the authenticated principal, or
// "" if the context carries none (auth.FromContext is nil-safe, but the
// zero-value Principal itself has no meaning here, so a nil check is used
// instead).
func actorID(ctx context.Context) string {
	if p := auth.FromContext(ctx); p != nil {
		return p.UserID
	}
	return ""
}

type createDocumentRequest struct {
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	IssuedAt string `json:"issued_at"`
	OwnerID  string `json:"owner_id"`
}

// Create is the centerpiece of this example: it shows why
// worker.Queue.EnqueueTx exists at all, instead of just Enqueue.
//
// A single tx.WithTx call creates the document row, opens its workflow
// case, records its audit entry, and enqueues the notify_reviewers job --
// all four writes share one database transaction, so they either ALL commit
// or NONE do. If EnqueueTx did not exist and this handler called Enqueue
// after the transaction committed instead, a crash between the commit and
// the Enqueue call would leave a document with a notification silently
// never sent, and no record that one was ever owed. Calling a plain Enqueue
// before the commit would be worse: a worker could pick up and process the
// job for a document that a later error in this same handler then rolls
// back entirely, notifying reviewers about a document that was never
// created. EnqueueTx removes both failure windows: River inserts the job
// row inside the SAME transaction (the Transactional Outbox pattern), so
// the job becomes visible to a worker if and only if the whole transaction
// commits -- exactly like the document row, the workflow case, and the
// audit entry it is enqueued alongside.
func (h *DocumentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createDocumentRequest
	if err := request.JSON(w, r, &req); err != nil {
		// request.JSON already wrote the error response.
		return
	}

	fields := request.FieldErrors{}
	if req.Title == "" || stringutil.Normalize(req.Title) == "" {
		fields["title"] = "title is required"
	}
	kindOK := false
	for _, k := range DocumentKinds {
		if req.Kind == k {
			kindOK = true
			break
		}
	}
	if !kindOK {
		fields["kind"] = "kind must be one of contract, invoice, report"
	}
	if req.OwnerID == "" {
		fields["owner_id"] = "owner_id is required"
	}
	issuedAt, dateErr := stringutil.ParseFlexibleDate(req.IssuedAt)
	if dateErr != nil {
		fields["issued_at"] = "issued_at must be a valid date (DD/MM/YYYY, DD-MM-YYYY or YYYY-MM-DD)"
	}
	if len(fields) > 0 {
		response.ValidationError(w, r, fields)
		return
	}

	doc := &Document{
		ID:        uuid.New(),
		Title:     req.Title,
		Kind:      req.Kind,
		IssuedAt:  issuedAt,
		OwnerID:   req.OwnerID,
		CreatedAt: time.Now(),
	}

	err := h.tx.WithTx(r.Context(), func(txCtx context.Context) error {
		db := pgxtx.DBFromContext(txCtx, h.pool)

		if err := h.store.Create(txCtx, db, doc); err != nil {
			return err
		}

		wfCase, err := h.engine.Open(txCtx, db, workflow.OpenInput{
			Definition: ApprovalDefinitionName,
			Domain:     DocumentDomain,
			ExternalID: doc.ID.String(),
			ActorID:    actorID(txCtx),
		})
		if err != nil {
			return err
		}

		// audit.Recorder.Record reads the acting principal and the request's
		// metadata (request ID, IP, User-Agent) from context BY ITSELF -- see
		// its doc comment -- so this call passes neither explicitly; both
		// come from txCtx via auth.FromContext and reqctx, the same way
		// logger.Logger.WithContext reads the request ID.
		if err := h.recorder.Record(txCtx, db, audit.SourceHTTP, audit.ActionCreate,
			"document", doc.ID.String(),
			audit.WithSubject(doc),
			audit.WithAggregate("workflow_case", wfCase.ID.String()),
		); err != nil {
			return err
		}

		// EnqueueTx (not Enqueue) is what makes this job part of the same
		// transaction as the three writes above -- see the doc comment on
		// Create.
		if _, err := h.queue.EnqueueTx(txCtx, pgxtx.TxFromContext(txCtx), NotifyReviewersArgs{
			DocumentID: doc.ID.String(),
			Title:      doc.Title,
		}, nil); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "create document", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to create document")
		return
	}

	response.JSON(w, http.StatusCreated, doc)
}

// Get retrieves a single document by ID.
func (h *DocumentHandler) Get(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	// pgxtx.DBFromContext returns the pool here, since no transaction is
	// active on a bare GET request -- this is the exact same call Create
	// makes inside tx.WithTx, where it instead returns the live transaction.
	// That is why DocumentStore never needs a "transactional" and a
	// "standalone" variant of each method.
	db := pgxtx.DBFromContext(r.Context(), h.pool)

	doc, err := h.store.GetByID(r.Context(), db, id)
	if err != nil {
		if errors.Is(err, ErrDocumentNotFound) {
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "document not found")
			return
		}
		h.logger.ErrorContext(r.Context(), "get document", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to get document")
		return
	}

	response.JSON(w, http.StatusOK, doc)
}

// List returns a paginated page of documents.
func (h *DocumentHandler) List(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	limit := v.IntQuery(r, "limit", 20)
	limit = v.MaxInt("limit", limit, 100)
	offset := v.IntQuery(r, "offset", 0)
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	db := pgxtx.DBFromContext(r.Context(), h.pool)
	docs, total, err := h.store.List(r.Context(), db, limit, offset)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "list documents", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to list documents")
		return
	}

	response.JSONList(w, http.StatusOK, docs, total, limit, offset)
}

type transitionRequest struct {
	Action string `json:"action"`
}

var transitionActions = []string{"submit", "approve", "reject", "return"}

// Transition applies a workflow action to a document's case.
func (h *DocumentHandler) Transition(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")

	var body transitionRequest
	if err := request.JSON(w, r, &body); err != nil {
		return
	}
	body.Action = v.Enum("action", body.Action, transitionActions)
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	err := h.tx.WithTx(r.Context(), func(txCtx context.Context) error {
		db := pgxtx.DBFromContext(txCtx, h.pool)

		if _, err := h.engine.Move(txCtx, db, workflow.MoveInput{
			CaseID:  id,
			Action:  body.Action,
			ActorID: actorID(txCtx),
		}); err != nil {
			return err
		}

		return h.recorder.Record(txCtx, db, audit.SourceHTTP, audit.ActionExecute,
			"document", id.String(),
			audit.WithOperationName("document_"+body.Action),
		)
	})
	if err != nil {
		switch {
		case errors.Is(err, workflow.ErrCaseNotFound):
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "document case not found")
		case errors.Is(err, workflow.ErrInvalidTransition), errors.Is(err, workflow.ErrGuardRejected):
			response.Error(w, r, http.StatusConflict, response.CodeConflict, "transition not allowed")
		default:
			h.logger.ErrorContext(r.Context(), "transition document", "error", err)
			response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to transition document")
		}
		return
	}

	response.JSONWithMessage(w, http.StatusOK, "transition applied", map[string]string{"action": body.Action})
}

// Claim assigns a document's case to the authenticated caller.
func (h *DocumentHandler) Claim(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	actor := actorID(r.Context())

	err := h.tx.WithTx(r.Context(), func(txCtx context.Context) error {
		db := pgxtx.DBFromContext(txCtx, h.pool)
		_, err := h.engine.Claim(txCtx, db, id, actor)
		return err
	})
	if err != nil {
		if errors.Is(err, workflow.ErrCaseNotFound) {
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "document case not found")
			return
		}
		h.logger.ErrorContext(r.Context(), "claim document", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to claim document")
		return
	}

	response.JSONWithMessage(w, http.StatusOK, "document claimed", nil)
}

// History returns every workflow event recorded for a document's case.
func (h *DocumentHandler) History(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	db := pgxtx.DBFromContext(r.Context(), h.pool)
	events, err := h.engine.History(r.Context(), db, id)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "document history", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to get document history")
		return
	}

	response.JSON(w, http.StatusOK, events)
}

// Inbox lists unclaimed open cases in the document domain.
func (h *DocumentHandler) Inbox(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	limit := v.IntQuery(r, "limit", 20)
	limit = v.MaxInt("limit", limit, 100)
	offset := v.IntQuery(r, "offset", 0)
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	db := pgxtx.DBFromContext(r.Context(), h.pool)
	cases, err := h.engine.Inbox(r.Context(), db, workflow.InboxFilter{
		Domain:     DocumentDomain,
		Unassigned: true,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "document inbox", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to get inbox")
		return
	}

	response.JSON(w, http.StatusOK, cases)
}

// maxUploadBytes caps an uploaded document body. http.MaxBytesReader
// enforces it on the server side, so a client cannot exhaust memory by
// streaming an unbounded body.
const maxUploadBytes = 25 << 20 // 25 MiB

// UploadFile attaches the raw request body to an existing document through
// the storage port, then records the attachment in the audit trail.
//
// This is the write half of the storage port; DownloadURL is the read half.
// Note what the handler does NOT know: whether the bytes land in S3, MinIO,
// DigitalOcean Spaces or -- as in this example -- a map in memory. It holds
// a storage.Storage, and main.go decides which implementation that is.
//
// The audit entry uses audit.WithChange(before, after) rather than
// WithSubject: both snapshots are captured, so the trail shows the document
// exactly as it was before the key was attached and exactly as it is after.
// That is what the Auditable interface on Document buys.
func (h *DocumentHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	db := pgxtx.DBFromContext(r.Context(), h.pool)

	before, err := h.store.GetByID(r.Context(), db, id)
	if err != nil {
		if errors.Is(err, ErrDocumentNotFound) {
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "document not found")
			return
		}
		h.logger.ErrorContext(r.Context(), "upload file: get document", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to get document")
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	body := http.MaxBytesReader(w, r.Body, maxUploadBytes)
	defer func() { _ = body.Close() }()

	key := StorageKeyFor(before.ID)

	// The object is uploaded BEFORE the transaction opens, on purpose: an
	// object store is not transactional, so it cannot join the database
	// transaction below. Doing it first means the worst failure leaves an
	// orphaned object that no row references -- recoverable by a sweeper --
	// instead of a row pointing at an object that was never written.
	if err := h.storage.Upload(r.Context(), &storage.UploadInput{
		Key:         key,
		Body:        body,
		ContentType: contentType,
	}); err != nil {
		h.logger.ErrorContext(r.Context(), "upload file: storage", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to store file")
		return
	}

	after := *before
	after.StorageKey = key

	err = h.tx.WithTx(r.Context(), func(txCtx context.Context) error {
		txDB := pgxtx.DBFromContext(txCtx, h.pool)

		if err := h.store.UpdateStorageKey(txCtx, txDB, before.ID, key); err != nil {
			return err
		}

		return h.recorder.Record(txCtx, txDB, audit.SourceHTTP, audit.ActionUpdate,
			"document", before.ID.String(),
			audit.WithChange(before, &after),
			audit.WithOperationName("document_attach_file"),
		)
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "upload file: persist", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to attach file")
		return
	}

	response.JSONWithMessage(w, http.StatusOK, "file attached", &after)
}

// DownloadURL returns a time-limited URL for downloading a document's
// stored object.
func (h *DocumentHandler) DownloadURL(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	db := pgxtx.DBFromContext(r.Context(), h.pool)
	doc, err := h.store.GetByID(r.Context(), db, id)
	if err != nil {
		if errors.Is(err, ErrDocumentNotFound) {
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "document not found")
			return
		}
		h.logger.ErrorContext(r.Context(), "download url: get document", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to get document")
		return
	}

	// A document with no attached object has nothing to presign. Returning
	// 409 rather than 404 is deliberate: the document exists, its file does
	// not -- see UploadFile.
	if doc.StorageKey == "" {
		response.Error(w, r, http.StatusConflict, response.CodeConflict, "document has no file attached")
		return
	}

	url, err := h.storage.PresignedGetURL(r.Context(), doc.StorageKey, 15*time.Minute)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "download url: presign", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to generate download url")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"url": url})
}
