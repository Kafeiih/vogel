package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/idempotency"
	idemhttp "github.com/kafeiih/vogel/idempotency/httpx"
	"github.com/kafeiih/vogel/pgxtx"
)

// fakeStore records the calls the middleware makes and answers Reserve with
// reserve (a fresh token when nil).
type fakeStore struct {
	mu        sync.Mutex
	reserve   func(idempotency.Request) (idempotency.Reservation, error)
	reserved  []idempotency.Request
	completed []idempotency.Response
	released  int
	token     uuid.UUID
	lease     time.Duration
}

func (f *fakeStore) Reserve(_ context.Context, _ pgxtx.DBTX, req idempotency.Request, lease time.Duration) (idempotency.Reservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserved = append(f.reserved, req)
	f.lease = lease
	if f.reserve != nil {
		return f.reserve(req)
	}
	f.token = uuid.New()
	return idempotency.Reservation{Token: f.token}, nil
}

func (f *fakeStore) Complete(_ context.Context, _ pgxtx.DBTX, _ idempotency.Request, token uuid.UUID, resp idempotency.Response) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if token != f.token {
		return errors.New("wrong token")
	}
	f.completed = append(f.completed, resp)
	return nil
}

func (f *fakeStore) Release(_ context.Context, _ pgxtx.DBTX, _ idempotency.Request, token uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if token != f.token {
		return errors.New("wrong token")
	}
	f.released++
	return nil
}

func newRequest(body, key, user string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(body))
	if key != "" {
		r.Header.Set(idemhttp.HeaderKey, key)
	}
	if user != "" {
		r = r.WithContext(auth.WithPrincipal(r.Context(), &auth.Principal{UserID: user}))
	}
	return r
}

// created answers 201 with headers and echoes the body it read, so a test
// sees that the middleware restored it.
func created(calls *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/payments/1")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "secret"})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body)
	})
}

func serve(t *testing.T, cfg idemhttp.Config, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	rec := httptest.NewRecorder()
	idemhttp.Middleware(cfg)(h).ServeHTTP(rec, r)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body response.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Code
}

func TestMiddleware_WithoutKeyPassesThrough(t *testing.T) {
	store := &fakeStore{}
	calls := 0
	rec := serve(t, idemhttp.Config{Store: store}, created(&calls), newRequest(`{}`, "", "user-1"))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, 1, calls)
	assert.Empty(t, store.reserved)
}

func TestMiddleware_RequiredKeyIsEnforced(t *testing.T) {
	store := &fakeStore{}
	calls := 0
	rec := serve(t, idemhttp.Config{Store: store, Required: true}, created(&calls), newRequest(`{}`, "", "user-1"))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, response.CodeValidationError, errorCode(t, rec))
	assert.Zero(t, calls)
}

func TestMiddleware_WithoutPrincipalPassesThrough(t *testing.T) {
	store := &fakeStore{}
	calls := 0
	rec := serve(t, idemhttp.Config{Store: store, Required: true}, created(&calls), newRequest(`{}`, "k-1", ""))
	assert.Equal(t, http.StatusCreated, rec.Code, "authentication is another middleware's job")
	assert.Equal(t, 1, calls)
	assert.Empty(t, store.reserved)
}

func TestMiddleware_InvalidKeyIsRejected(t *testing.T) {
	store := &fakeStore{}
	calls := 0
	rec := serve(t, idemhttp.Config{Store: store}, created(&calls), newRequest(`{}`, strings.Repeat("k", idempotency.MaxKeyLength+1), "user-1"))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, response.CodeValidationError, errorCode(t, rec))
	assert.Zero(t, calls)
	assert.Empty(t, store.reserved)
}

func TestMiddleware_FirstRequestRunsAndStoresTheResponse(t *testing.T) {
	store := &fakeStore{}
	calls := 0
	rec := serve(t, idemhttp.Config{Store: store}, created(&calls), newRequest(`{"amount":100}`, "k-1", "user-1"))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, `{"amount":100}`, rec.Body.String(), "the handler read the body the middleware fingerprinted")
	assert.NotEmpty(t, rec.Header().Get("Set-Cookie"), "the live response keeps its cookie")
	assert.Empty(t, rec.Header().Get(idemhttp.HeaderReplayed))
	assert.Equal(t, 1, calls)

	require.Len(t, store.reserved, 1)
	assert.Equal(t, "user-1 POST /payments", store.reserved[0].Scope)
	assert.Equal(t, "k-1", store.reserved[0].Key)
	assert.Equal(t, idemhttp.DefaultLease, store.lease)
	require.Len(t, store.completed, 1)
	stored := store.completed[0]
	assert.Equal(t, http.StatusCreated, stored.Status)
	assert.Equal(t, []byte(`{"amount":100}`), stored.Body)
	assert.Equal(t, []string{"/payments/1"}, stored.Header["Location"])
	assert.NotContains(t, stored.Header, "Set-Cookie", "a cookie is never stored for replay")
}

func TestMiddleware_FingerprintCoversMethodPathAndBody(t *testing.T) {
	store := &fakeStore{}
	calls := 0
	h := created(&calls)
	serve(t, idemhttp.Config{Store: store}, h, newRequest(`{"amount":100}`, "k-1", "user-1"))
	serve(t, idemhttp.Config{Store: store}, h, newRequest(`{"amount":999}`, "k-1", "user-1"))
	other := newRequest(`{"amount":100}`, "k-1", "user-1")
	other.URL.Path = "/refunds"
	serve(t, idemhttp.Config{Store: store}, h, other)

	require.Len(t, store.reserved, 3)
	assert.NotEqual(t, store.reserved[0].Fingerprint, store.reserved[1].Fingerprint)
	assert.NotEqual(t, store.reserved[0].Fingerprint, store.reserved[2].Fingerprint)
	assert.Equal(t, "user-1 POST /refunds", store.reserved[2].Scope)
}

func TestMiddleware_ReplaysAStoredResponse(t *testing.T) {
	stored := idempotency.Response{Status: http.StatusCreated, Header: map[string][]string{"Content-Type": {"application/json"}, "Location": {"/payments/1"}}, Body: []byte(`{"id":1}`)}
	store := &fakeStore{reserve: func(idempotency.Request) (idempotency.Reservation, error) {
		return idempotency.Reservation{Replay: &stored}, nil
	}}
	calls := 0
	rec := serve(t, idemhttp.Config{Store: store}, created(&calls), newRequest(`{}`, "k-1", "user-1"))

	assert.Zero(t, calls, "a replay never runs the handler")
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, `{"id":1}`, rec.Body.String())
	assert.Equal(t, "/payments/1", rec.Header().Get("Location"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "true", rec.Header().Get(idemhttp.HeaderReplayed))
}

func TestMiddleware_MapsStoreErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"in progress", idempotency.ErrInProgress, http.StatusConflict, response.CodeConflict},
		{"key reused", idempotency.ErrKeyReused, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
		{"store down", errors.New("connection refused"), http.StatusServiceUnavailable, response.CodeServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{reserve: func(idempotency.Request) (idempotency.Reservation, error) {
				return idempotency.Reservation{}, tc.err
			}}
			calls := 0
			rec := serve(t, idemhttp.Config{Store: store}, created(&calls), newRequest(`{}`, "k-1", "user-1"))
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, errorCode(t, rec))
			assert.Zero(t, calls)
		})
	}
}

func TestMiddleware_ServerErrorReleasesTheKey(t *testing.T) {
	store := &fakeStore{}
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	rec := serve(t, idemhttp.Config{Store: store}, h, newRequest(`{}`, "k-1", "user-1"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, store.completed, "a 5xx is not stored")
	assert.Equal(t, 1, store.released, "so a retry runs the handler again")
}

func TestMiddleware_PanicReleasesTheKeyAndRepanics(t *testing.T) {
	store := &fakeStore{}
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	assert.PanicsWithValue(t, "boom", func() {
		serve(t, idemhttp.Config{Store: store}, h, newRequest(`{}`, "k-1", "user-1"))
	})
	assert.Equal(t, 1, store.released)
	assert.Empty(t, store.completed)
}

func TestMiddleware_ImplicitOKIsStored(t *testing.T) {
	store := &fakeStore{}
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("done")) })
	rec := serve(t, idemhttp.Config{Store: store}, h, newRequest(`{}`, "k-1", "user-1"))
	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, store.completed, 1)
	assert.Equal(t, http.StatusOK, store.completed[0].Status)
	assert.Equal(t, []byte("done"), store.completed[0].Body)
}

func TestMiddleware_OversizedResponseIsServedButNotStored(t *testing.T) {
	store := &fakeStore{}
	big := strings.Repeat("x", 2048)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(big)) })
	rec := serve(t, idemhttp.Config{Store: store, MaxResponseBytes: 1024}, h, newRequest(`{}`, "k-1", "user-1"))
	assert.Equal(t, big, rec.Body.String(), "the client still gets the whole response")
	assert.Empty(t, store.completed)
	assert.Equal(t, 1, store.released)
}

func TestMiddleware_OversizedRequestIsRejected(t *testing.T) {
	store := &fakeStore{}
	calls := 0
	rec := serve(t, idemhttp.Config{Store: store, MaxRequestBytes: 8}, created(&calls), newRequest(`{"amount":100}`, "k-1", "user-1"))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, response.CodeBodyTooLarge, errorCode(t, rec))
	assert.Zero(t, calls)
	assert.Empty(t, store.reserved)
}

func TestMiddleware_CustomLease(t *testing.T) {
	store := &fakeStore{}
	calls := 0
	serve(t, idemhttp.Config{Store: store, Lease: 5 * time.Minute}, created(&calls), newRequest(`{}`, "k-1", "user-1"))
	assert.Equal(t, 5*time.Minute, store.lease)
}
