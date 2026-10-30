package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/access"
	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
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
	wfRepo   workflow.Repository
	recorder *audit.Recorder
	queue    worker.Queue
	storage  storage.Storage
	tx       *pgxtx.PgxTxManager
	pool     *pgxpool.Pool
	guard    *access.Guard
	logger   *slog.Logger
}

// NewDocumentHandler builds a DocumentHandler from its dependencies.
//
// guard is used only by Delete, to demonstrate the per-instance
// authorization path -- see that method's doc comment. Every other handler
// here still relies solely on the coarse, router-level check that
// RequirePermission (or RequireAccess) already performs.
func NewDocumentHandler(
	store *DocumentStore,
	engine *workflow.Engine,
	wfRepo workflow.Repository,
	recorder *audit.Recorder,
	queue worker.Queue,
	fileStorage storage.Storage,
	tx *pgxtx.PgxTxManager,
	pool *pgxpool.Pool,
	guard *access.Guard,
	logger *slog.Logger,
) *DocumentHandler {
	return &DocumentHandler{
		store:    store,
		engine:   engine,
		wfRepo:   wfRepo,
		recorder: recorder,
		queue:    queue,
		storage:  fileStorage,
		tx:       tx,
		pool:     pool,
		guard:    guard,
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

// maxTitleLength is the longest document title, in characters, that Create
// accepts. documents.title is unbounded TEXT, so without this cap a single
// request could store (and every later list, history and notification job
// would carry) a title of any size request.JSON's body limit lets through.
const maxTitleLength = 200

type createDocumentRequest struct {
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	IssuedAt string `json:"issued_at"`
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
//
// The owner is always the authenticated caller, never a client-supplied
// field: accepting owner_id from the body would let any writer create a
// document owned by someone else and thereby grant that user the
// owner-only delete permission (see fakeChecker). Because request.JSON
// disallows unknown fields, a body that still sends owner_id is rejected
// with 400 instead of being silently ignored.
func (h *DocumentHandler) Create(w http.ResponseWriter, r *http.Request) {
	ownerID := actorID(r.Context())
	if ownerID == "" {
		// Unreachable behind vmw.Authenticate; fail closed regardless.
		response.Error(w, r, http.StatusUnauthorized, response.CodeUnauthorized, "authentication required")
		return
	}

	var req createDocumentRequest
	if err := request.JSON(w, r, &req); err != nil {
		// request.JSON already wrote the error response.
		return
	}

	v := request.NewValidator()
	if req.Title == "" || stringutil.Normalize(req.Title) == "" {
		v.AddError("title", "title is required")
	} else {
		// MaxLen counts runes of the NFC form, so an accented title is not
		// penalised for its UTF-8 byte length or for arriving decomposed;
		// keep the NFC form it returns.
		req.Title = v.MaxLen("title", req.Title, maxTitleLength)
	}
	kindOK := false
	for _, k := range DocumentKinds {
		if req.Kind == k {
			kindOK = true
			break
		}
	}
	if !kindOK {
		v.AddError("kind", "kind must be one of contract, invoice, report")
	}
	issuedAt, dateErr := stringutil.ParseFlexibleDate(req.IssuedAt)
	if dateErr != nil {
		v.AddError("issued_at", "issued_at must be a valid date (DD/MM/YYYY, DD-MM-YYYY or YYYY-MM-DD)")
	}
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	doc := &Document{
		ID:         uuid.New(),
		Title:      req.Title,
		Kind:       req.Kind,
		IssuedAt:   issuedAt,
		FileStatus: FileStatusNone,
		OwnerID:    ownerID,
		CreatedAt:  time.Now(),
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
			Actor:      workflow.Actor{Kind: workflow.ActorHuman, ID: actorID(txCtx)},
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

// Delete removes a document. It is the one route in this example that
// demonstrates the per-instance authorization path from vogel/access,
// instead of relying only on RequirePermission's coarse, router-level check.
//
// The router mounts this behind vmw.RequireAccess(h.guard, ...), which runs
// BEFORE this method and can only check the principal against the resource
// kind and the URL's {id} -- it has no idea yet whether that document is a
// "report" (see fakeChecker.IsAllowed), because it has not loaded the row.
// That is exactly the decision this method makes AFTER GetByID, by folding
// the loaded document's Kind into authz.Resource.Attr and calling
// h.guard.Check again: the same authz.Checker backs both calls, but only
// this second one has the data to deny deleting a report even when the
// caller owns it.
//
// The ownership half of the decision (fakeChecker.IsAllowed's
// owned_document_ids branch) comes from access.WithPrincipalAttributes,
// resolved from DocumentStore.OwnedIDs in main.go. Because
// vmw.RequireAccess already installed access.WithRequestScope on this
// request before its own check ran, h.guard.Check here reuses that same
// resolved principal attribute instead of querying OwnedIDs a second time --
// see access.WithRequestScope's doc comment.
func (h *DocumentHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
		h.logger.ErrorContext(r.Context(), "delete document: get document", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to get document")
		return
	}

	resource := authz.Resource{
		Kind: "documents:document",
		ID:   doc.ID.String(),
		Attr: map[string]any{"owner": doc.OwnerID, "kind": doc.Kind},
	}
	checkErr := h.guard.Check(r.Context(), resource, "delete")
	if checkErr != nil {
		// WriteAccessErrorFor logs which document was denied (kind, ID and
		// action, never the Attr data), unlike WriteAccessError.
		if vmw.WriteAccessErrorFor(w, r, checkErr, h.logger, resource, "delete") {
			return
		}
		// WriteAccessErrorFor only recognizes the three access sentinels; any
		// other error here is this handler's own to report.
		h.logger.ErrorContext(r.Context(), "delete document: check access", "error", checkErr)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to authorize delete")
		return
	}

	if err := h.store.Delete(r.Context(), db, doc.ID); err != nil {
		h.logger.ErrorContext(r.Context(), "delete document", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to delete document")
		return
	}

	response.JSONWithMessage(w, http.StatusOK, "document deleted", nil)
}

// List returns a paginated page of documents.
func (h *DocumentHandler) List(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	limit := v.IntQuery(r, "limit", 20)
	limit = v.MaxInt("limit", limit, 100)
	offset := v.IntQuery(r, "offset", 0)
	// Same bound as Inbox, for the same reason: Postgres reads and discards
	// every row an OFFSET skips, so an unbounded one lets a single request
	// make the database walk the whole table. Deeper pages should filter
	// instead of paging further.
	if offset > workflow.MaxListOffset {
		v.AddError("offset", fmt.Sprintf("offset must not exceed %d", workflow.MaxListOffset))
	}
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

// caseFor resolves the workflow case that tracks a document.
//
// It exists because a workflow.Case ID and a Document ID are different
// things, and every Engine method -- Move, Claim, Release, History -- is
// keyed by the CASE ID. The Engine deliberately exposes no lookup by domain
// reference, so an API whose routes are keyed by the domain object (which
// is the normal shape: a client holds /documents/{id}, not a case ID) must
// go through the workflow.Repository port to translate one into the other.
// That is why this handler holds both the engine and the repository.
//
// The alternative -- storing the case ID on the documents row -- would
// duplicate a link the workflow tables already own, and would have to be
// kept in sync by hand.
func (h *DocumentHandler) caseFor(ctx context.Context, db pgxtx.DBTX, documentID uuid.UUID) (*workflow.Case, error) {
	return h.wfRepo.GetByExternalID(ctx, db, "", DocumentDomain, documentID.String())
}

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

		wfCase, err := h.caseFor(txCtx, db, id)
		if err != nil {
			return err
		}

		if _, err := h.engine.Move(txCtx, db, workflow.MoveInput{
			CaseID: wfCase.ID,
			Action: body.Action,
			Actor:  workflow.Actor{Kind: workflow.ActorHuman, ID: actorID(txCtx)},
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
		case errors.Is(err, workflow.ErrCaseClosed):
			// A closed case is a client error, not a server one: the caller
			// asked for something the case's state no longer permits. Letting
			// it fall through to the default arm would report a 500 and log a
			// scary error for a perfectly ordinary race between two reviewers.
			response.Error(w, r, http.StatusConflict, response.CodeConflict, "document case is already closed")
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

		wfCase, err := h.caseFor(txCtx, db, id)
		if err != nil {
			return err
		}

		_, err = h.engine.Claim(txCtx, db, wfCase.ID, actor)
		return err
	})
	if err != nil {
		switch {
		case errors.Is(err, workflow.ErrCaseNotFound):
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "document case not found")
		case errors.Is(err, workflow.ErrCaseClosed):
			response.Error(w, r, http.StatusConflict, response.CodeConflict, "document case is already closed")
		case errors.Is(err, workflow.ErrAlreadyAssigned):
			// Two reviewers racing for the same case is expected traffic, not
			// a fault: the loser gets a 409, not a 500.
			response.Error(w, r, http.StatusConflict, response.CodeConflict, "document case is already claimed")
		default:
			h.logger.ErrorContext(r.Context(), "claim document", "error", err)
			response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to claim document")
		}
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

	wfCase, err := h.caseFor(r.Context(), db, id)
	if err != nil {
		if errors.Is(err, workflow.ErrCaseNotFound) {
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "document case not found")
			return
		}
		h.logger.ErrorContext(r.Context(), "document history: resolve case", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to get document history")
		return
	}

	events, err := h.engine.History(r.Context(), db, wfCase.ID)
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
	// The repository rejects an offset above workflow.MaxListOffset with
	// workflow.ErrOffsetOutOfRange; checking it here answers 400 instead of
	// surfacing that as a 500.
	if offset > workflow.MaxListOffset {
		v.AddError("offset", fmt.Sprintf("offset must not exceed %d", workflow.MaxListOffset))
	}
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}

	db := pgxtx.DBFromContext(r.Context(), h.pool)
	items, err := h.engine.Inbox(r.Context(), db, workflow.InboxFilter{
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

	// The document workflow never splits, so each work item is its case's
	// only token: the response stays a list of cases.
	cases := make([]workflow.Case, len(items))
	for i, it := range items {
		cases[i] = it.Case
	}
	response.JSON(w, http.StatusOK, cases)
}

// maxUploadBytes caps an uploaded document body. UploadURL refuses to sign
// a larger size, and the URL it signs carries the exact Content-Length, so
// the store itself refuses a body of any other size: neither this server
// nor the bucket ever accepts more.
const maxUploadBytes = 25 << 20 // 25 MiB

// uploadURLExpiry and downloadURLExpiry are how long the presigned URLs
// this API hands out stay valid. A presigned URL is a bearer credential
// nothing can revoke, so both are short: long enough for a client to start
// the transfer, not long enough to be worth stealing.
const (
	uploadURLExpiry   = 5 * time.Minute
	downloadURLExpiry = 5 * time.Minute
)

// allowedUploadTypes are the media types an upload may be: the document
// kinds this example stores. UploadURL only signs a URL for one of them, and
// validate_upload (ValidateUploadWorker) checks the uploaded content against
// them with storage.SniffContentType, since the type a client signs for
// says nothing about the bytes it then sends.
var allowedUploadTypes = []string{"application/pdf", "image/png", "image/jpeg", "text/plain"}

// disallowedUploadMessage is the 415 message, built from allowedUploadTypes
// so the list it gives the client cannot drift from what is accepted.
var disallowedUploadMessage = "file type not allowed: accepted types are " + strings.Join(allowedUploadTypes, ", ")

type uploadURLRequest struct {
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// uploadURLResponse tells the client exactly how to send its file: the
// method, the URL and the headers the URL was signed with. Sending any other
// Content-Type or a body of any other size makes the store refuse it.
type uploadURLResponse struct {
	URL       string            `json:"url"`
	Key       string            `json:"key"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"` // approximates the signed expiry
}

// UploadURL is step one of the presigned upload flow: it hands the client a
// short-lived URL to PUT the file straight to storage, so the file's bytes
// never pass through this server (step two is AttachFile; step three is
// the validate_upload job).
//
// The URL signs the content type and the exact size the client declared,
// both checked first: a type outside allowedUploadTypes is a 415, a size
// outside 1..maxUploadBytes a 400. Each upload gets its own key,
// documents/{id}/uploads/{uuid}, so a new upload never overwrites an object
// a previous one (or its validation job) is still using. Nothing is
// persisted: until the client confirms, the document is unchanged.
//
// The URL is a credential: it is returned to the caller and nowhere else --
// never logged, never put in an error, never audited.
func (h *DocumentHandler) UploadURL(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")

	var req uploadURLRequest
	if err := request.JSON(w, r, &req); err != nil {
		return
	}
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}
	// Exact match: the client must send this very Content-Type header, so
	// there is no room for parameters it would then have to repeat.
	if !slices.Contains(allowedUploadTypes, req.ContentType) {
		response.Error(w, r, http.StatusUnsupportedMediaType, response.CodeUnsupportedMediaType, disallowedUploadMessage)
		return
	}
	if req.Size < 1 || req.Size > maxUploadBytes {
		v.AddError("size", fmt.Sprintf("size must be between 1 and %d bytes", maxUploadBytes))
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
		h.logger.ErrorContext(r.Context(), "upload url: get document", "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to get document")
		return
	}

	key := newUploadKey(doc.ID)
	// expires_at is derived from the same expiry handed to the adapter and
	// a clock read just before signing, so it approximates the signed expiry
	// (the adapter reads its own clock; the two differ by the time signing
	// takes). A client should treat it as a deadline to start the PUT, not
	// as an exact instant.
	expiry := uploadURLExpiry
	signedAt := time.Now()
	url, err := h.storage.PresignedPutURL(r.Context(), key, req.ContentType, expiry, storage.WithContentLength(req.Size))
	if err != nil {
		h.logger.ErrorContext(r.Context(), "upload url: presign", "key", key, "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to generate upload url")
		return
	}

	response.JSON(w, http.StatusOK, uploadURLResponse{
		URL:       url,
		Key:       key,
		Method:    http.MethodPut,
		Headers:   map[string]string{"Content-Type": req.ContentType},
		ExpiresAt: signedAt.Add(expiry),
	})
}

// errUploadPending is AttachFile's signal, from inside its transaction, that
// the document's previous upload has not been validated yet (409).
var errUploadPending = errors.New("previous upload still pending")

// stalePendingAfter is how long a document may stay pending before a confirm
// treats its upload as abandoned and takes the row over. It sits well above
// validate_upload's bounded retry window (about 12 minutes, see
// validateUploadMaxAttempts), so a job that is merely retrying is never
// overtaken.
const stalePendingAfter = time.Hour

type attachFileRequest struct {
	Key string `json:"key"`
}

// AttachFile is step two of the presigned upload flow: the client, having
// PUT its file with the URL from UploadURL, confirms the key it uploaded
// to. The key must be one UploadURL could have issued for this document
// (documents/{id}/uploads/{uuid}), so a client cannot attach another
// document's object, nor a final documents/{id}/file/ key, which only the
// validate_upload job sets.
//
// One transaction points the document at the key with its file pending,
// enqueues validate_upload with worker.Queue.EnqueueTx, and records the
// audit entry -- the same all-or-nothing shape as Create. The object itself
// was written before any of this and outside the transaction, by the
// client: an object store cannot join a database transaction, and this
// order means the worst failure leaves an orphaned object no row
// references, never a row pointing at a key nobody presigned for this
// document. Whether the object really exists and holds an allowed type is
// the job's call, not this handler's: it answers 202 with the document as
// it now stands, pending. While a previous upload is still pending it
// answers 409 and changes nothing: the client retries once that upload is
// resolved.
//
// The audit entry uses audit.WithChange(before, after) rather than
// WithSubject: both snapshots are captured, so the trail shows the document
// exactly as it was before the key was attached and exactly as it is after.
// That is what the Auditable interface on Document buys.
func (h *DocumentHandler) AttachFile(w http.ResponseWriter, r *http.Request) {
	v := request.NewValidator()
	id := v.UUIDParam(r, "id")

	var req attachFileRequest
	if err := request.JSON(w, r, &req); err != nil {
		return
	}
	if v.HasErrors() {
		v.WriteErrors(w, r)
		return
	}
	// Only the canonical UUID form UploadURL issues is accepted, so the key
	// stays exactly documents/{id}/uploads/{uuid}.
	prefix := uploadKeyPrefix(id)
	suffix, ok := strings.CutPrefix(req.Key, prefix)
	if parsed, err := uuid.Parse(suffix); !ok || err != nil || parsed.String() != suffix {
		v.AddError("key", "key must be an upload key issued for this document ("+prefix+"<uuid>)")
		v.WriteErrors(w, r)
		return
	}

	var after Document
	err := h.tx.WithTx(r.Context(), func(txCtx context.Context) error {
		db := pgxtx.DBFromContext(txCtx, h.pool)

		// FOR UPDATE: what the job is told about the previous file must
		// come from the row as this transaction will overwrite it. A plain
		// read could see a state another transaction (a job making a file
		// ready, a parallel confirm) is about to replace, and lose track of
		// the file that transaction commits.
		before, err := h.store.GetByIDForUpdate(txCtx, db, id)
		if err != nil {
			return err
		}
		// One upload is validated at a time: while one is pending, its
		// job owns the row's file, and a second confirm answers 409. A row
		// pending for longer than stalePendingAfter -- far past the bounded
		// retries of validate_upload -- has lost its job (a panic, an
		// operator cancel, a database outage on the last attempt) and is
		// taken over instead, keeping the previous file it holds. The
		// abandoned job, should it still run, finds the row moved on and
		// only cleans up its own objects.
		takeover := false
		if before.FileStatus == FileStatusPending {
			stale, err := h.store.PendingStale(txCtx, db, id, stalePendingAfter)
			if err != nil {
				return err
			}
			if !stale {
				return errUploadPending
			}
			takeover = true
			h.logger.WarnContext(txCtx, "taking over a stale pending upload",
				"document_id", id, "abandoned_key", before.StorageKey, "pending_since", *before.FilePendingSince)
		}
		// A ready file stays on the row as the previous file, with its type:
		// downloads keep serving it while this upload is validated, and the
		// job puts it back if this upload is rejected (or retires it if
		// this upload replaces it). A rejected or empty row has none.
		previousKey, previousContentType := "", ""
		switch {
		case before.FileStatus == FileStatusReady && before.StorageKey != "":
			previousKey, previousContentType = before.StorageKey, before.FileContentType
		case takeover:
			previousKey, previousContentType = before.PreviousStorageKey, before.PreviousContentType
		}

		after = *before
		after.StorageKey = req.Key
		after.FileStatus = FileStatusPending
		after.FileContentType = ""
		after.PreviousStorageKey = previousKey
		after.PreviousContentType = previousContentType
		pendingSince := time.Now() // the row records the database's now()
		after.FilePendingSince = &pendingSince

		if err := h.store.AttachPendingFile(txCtx, db, id, req.Key, previousKey, previousContentType); err != nil {
			return err
		}

		// EnqueueTx: the job exists if and only if the pending state it
		// will resolve commits -- see Create.
		if _, err := h.queue.EnqueueTx(txCtx, pgxtx.TxFromContext(txCtx), ValidateUploadArgs{
			DocumentID: id.String(),
			Key:        req.Key,
		}, &river.InsertOpts{MaxAttempts: validateUploadMaxAttempts}); err != nil {
			return err
		}

		return h.recorder.Record(txCtx, db, audit.SourceHTTP, audit.ActionUpdate,
			"document", id.String(),
			audit.WithChange(before, &after),
			audit.WithOperationName("document_attach_file"),
		)
	})
	if err != nil {
		if errors.Is(err, ErrDocumentNotFound) {
			response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "document not found")
			return
		}
		if errors.Is(err, errUploadPending) {
			response.Error(w, r, http.StatusConflict, response.CodeConflict, "a previous upload is still being validated")
			return
		}
		h.logger.ErrorContext(r.Context(), "attach file", "key", req.Key, "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to attach file")
		return
	}

	response.JSONWithMessage(w, http.StatusAccepted, "file pending validation", &after)
}

// fileNotReadyMessages is DownloadURL's 409 message per file status: the
// document exists, but it has no file a client may download yet.
var fileNotReadyMessages = map[string]string{
	FileStatusNone:     "document has no file attached",
	FileStatusPending:  "document file is still being validated",
	FileStatusRejected: "document file was rejected",
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

	// Only a file validate_upload accepted may be downloaded: a pending
	// upload has not been inspected and a rejected one was deleted. While a
	// replacement is pending, the file it would replace is still the
	// document's validated file, so that one is served. Otherwise 409 rather
	// than 404 is deliberate: the document exists, a downloadable file does
	// not (yet).
	key, contentType := doc.StorageKey, doc.FileContentType
	switch {
	case doc.FileStatus == FileStatusReady:
	case doc.FileStatus == FileStatusPending && doc.PreviousStorageKey != "":
		key, contentType = doc.PreviousStorageKey, doc.PreviousContentType
	default:
		msg, ok := fileNotReadyMessages[doc.FileStatus]
		if !ok {
			msg = "document file is not ready"
		}
		response.Error(w, r, http.StatusConflict, response.CodeConflict, msg)
		return
	}

	// No disposition option: the port defaults to Content-Disposition:
	// attachment, so the browser downloads the object under its key rather
	// than rendering it on the bucket origin. Document keeps no original
	// filename; a record that did would pass storage.AsAttachment(name).
	// The response type is the one validate_upload sniffed from the content,
	// never the one the client declared. A row migration 002 marked ready
	// (attached by the old proxying upload) has a NULL type, read as "":
	// WithResponseContentType("") keeps storage.DefaultResponseContentType,
	// so such a file is served as application/octet-stream.
	url, err := h.storage.PresignedGetURL(r.Context(), key, downloadURLExpiry,
		storage.WithResponseContentType(contentType))
	if err != nil {
		h.logger.ErrorContext(r.Context(), "download url: presign", "key", key, "error", err)
		response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "failed to generate download url")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"url": url})
}
