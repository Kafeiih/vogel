package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/reqctx"
)

// logLine runs h against one request and returns the single structured log
// record StructuredLogger emitted for it.
func logLine(t *testing.T, r *http.Request, opts ...LoggerOption) map[string]any {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := StructuredLogger(logger, opts...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v (%q)", err, buf.String())
	}
	return got
}

// RequestContext's doc comment promises a log line and an audit_log row for
// the same request are provably linked, because both read the request ID from
// reqctx. StructuredLogger used to read chi's GetReqID instead, so the access
// log only agreed with audit by coincidence — both happened to derive from the
// same upstream value. Any context carrying a request ID that did not come
// from chi (a worker deriving one from the request that spawned it, a test)
// logged an empty request_id while audit recorded the real one.
func TestStructuredLogger_ReadsRequestIDFromReqctx(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/things", nil)
	r = r.WithContext(reqctx.WithRequestID(r.Context(), "req-abc-123"))

	got := logLine(t, r)

	if got["request_id"] != "req-abc-123" {
		t.Errorf("request_id = %v, want %q", got["request_id"], "req-abc-123")
	}
}

// With no request ID anywhere the field is present and empty rather than
// missing, so a log pipeline sees a consistent shape.
func TestStructuredLogger_NoRequestID_LogsEmpty(t *testing.T) {
	got := logLine(t, httptest.NewRequest(http.MethodGet, "/things", nil))

	if got["request_id"] != "" {
		t.Errorf("request_id = %v, want empty", got["request_id"])
	}
}

// The client-supplied correlation ID is logged under its own key, so a
// client's value can never be mistaken for the server-generated request_id;
// without one the key is left out.
func TestStructuredLogger_ClientRequestID(t *testing.T) {
	tests := []struct {
		name     string
		clientID string
		want     any // nil means the key must be absent
	}{
		{name: "client ID present", clientID: "client-xyz", want: "client-xyz"},
		{name: "client ID absent", clientID: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/things", nil)
			ctx := reqctx.WithRequestID(r.Context(), "req-abc-123")
			if tt.clientID != "" {
				ctx = reqctx.WithClientRequestID(ctx, tt.clientID)
			}
			got := logLine(t, r.WithContext(ctx))

			v, ok := got["client_request_id"]
			if tt.want == nil {
				if ok {
					t.Errorf("client_request_id = %v, want the key absent", v)
				}
				return
			}
			if v != tt.want {
				t.Errorf("client_request_id = %v, want %v", v, tt.want)
			}
			if got["request_id"] != "req-abc-123" {
				t.Errorf("request_id = %v, want the server ID req-abc-123", got["request_id"])
			}
		})
	}
}

// accessLogFor runs one request through the production order — RequestContext
// (when withRequestContext), then StructuredLogger, then Authenticate — and
// returns the access-log record StructuredLogger emitted plus the response
// status.
func accessLogFor(t *testing.T, fa *fakeAuthenticator, authHeader string, withRequestContext bool) (map[string]any, int) {
	t.Helper()

	var buf bytes.Buffer
	accessLogger := slog.New(slog.NewJSONHandler(&buf, nil))
	authLogger := slog.New(slog.DiscardHandler)

	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h = Authenticate(fa, authLogger)(h)
	h = StructuredLogger(accessLogger)(h)
	if withRequestContext {
		h = RequestContext(h)
	}

	r := httptest.NewRequest(http.MethodGet, "/things", nil)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("access log is not one JSON record: %v (%q)", err, buf.String())
	}
	return got, rec.Code
}

// StructuredLogger wraps Authenticate, and Authenticate stores the Principal
// on a derived request the access logger never sees. The user slot
// RequestContext installs is what carries the authenticated user's ID back
// out to the access log — and only the ID: username and roles stay out.
func TestStructuredLogger_LogsAuthenticatedUserID(t *testing.T) {
	fa := &fakeAuthenticator{principal: &auth.Principal{UserID: "u-42", Username: "alice", Roles: []string{"admin"}}}

	got, status := accessLogFor(t, fa, "Bearer sometoken", true)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got["user_id"] != "u-42" {
		t.Errorf("user_id = %v, want %q", got["user_id"], "u-42")
	}
	for _, key := range []string{"username", "roles"} {
		if v, ok := got[key]; ok {
			t.Errorf("access log carries %q = %v; only user_id may be logged", key, v)
		}
	}
	line, _ := json.Marshal(got)
	if strings.Contains(string(line), "alice") || strings.Contains(string(line), "sometoken") {
		t.Errorf("access log leaks the username or the token: %s", line)
	}
}

// A rejected request has no principal, so the key is left out rather than
// logged empty, matching client_request_id.
func TestStructuredLogger_UnauthenticatedRequest_OmitsUserID(t *testing.T) {
	got, status := accessLogFor(t, &fakeAuthenticator{}, "", true)

	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
	if v, ok := got["user_id"]; ok {
		t.Errorf("user_id = %v, want the key absent", v)
	}
}

// Without RequestContext there is no slot: Authenticate must still succeed
// and the access log simply has no user_id.
func TestStructuredLogger_NoUserSlot_AuthenticateStillWorks(t *testing.T) {
	fa := &fakeAuthenticator{principal: &auth.Principal{UserID: "u-42"}}

	got, status := accessLogFor(t, fa, "Bearer sometoken", false)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if v, ok := got["user_id"]; ok {
		t.Errorf("user_id = %v, want the key absent without a user slot", v)
	}
}

func TestStructuredLogger_ClientInfoDefaultsUnchanged(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = "203.0.113.42:51234"
	r.Header.Set("User-Agent", "curl/8.0")

	got := logLine(t, r)
	if got["remote_addr"] != "203.0.113.42:51234" {
		t.Errorf("remote_addr = %v, want the full address", got["remote_addr"])
	}
	if got["user_agent"] != "curl/8.0" {
		t.Errorf("user_agent = %v, want curl/8.0", got["user_agent"])
	}
}

func TestStructuredLogger_RemoteAddrModes(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		mode   RemoteAddrMode
		want   any // nil means the key is absent
	}{
		{name: "full", remote: "203.0.113.42:51234", mode: RemoteAddrFull, want: "203.0.113.42:51234"},
		{name: "truncated IPv4 with port", remote: "203.0.113.42:51234", mode: RemoteAddrTruncated, want: "203.0.113.0"},
		{name: "truncated IPv4 without port (behind RealIP)", remote: "203.0.113.42", mode: RemoteAddrTruncated, want: "203.0.113.0"},
		{name: "truncated IPv6", remote: "[2001:db8:abcd:12:1:2:3:4]:443", mode: RemoteAddrTruncated, want: "2001:db8:abcd::"},
		{name: "truncated IPv4-mapped IPv6", remote: "[::ffff:203.0.113.42]:443", mode: RemoteAddrTruncated, want: "203.0.113.0"},
		{name: "truncated unparsable address is dropped", remote: "not-an-ip", mode: RemoteAddrTruncated, want: nil},
		{name: "omitted", remote: "203.0.113.42:51234", mode: RemoteAddrOmitted, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.RemoteAddr = tt.remote
			got := logLine(t, r, WithRemoteAddr(tt.mode))
			v, ok := got["remote_addr"]
			if tt.want == nil {
				if ok {
					t.Errorf("remote_addr = %v, want it absent", v)
				}
				return
			}
			if v != tt.want {
				t.Errorf("remote_addr = %v, want %v", v, tt.want)
			}
		})
	}
}

func TestStructuredLogger_WithoutUserAgent(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("User-Agent", "curl/8.0")

	got := logLine(t, r, WithoutUserAgent())
	if v, ok := got["user_agent"]; ok {
		t.Errorf("user_agent = %v, want it absent", v)
	}
	if got["path"] != "/x" {
		t.Errorf("path = %v, want /x (the rest of the line is unchanged)", got["path"])
	}
}

// An unknown RemoteAddrMode fails closed: the address is left out.
func TestStructuredLogger_UnknownRemoteAddrModeOmitsAddress(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = "203.0.113.42:51234"
	got := logLine(t, r, WithRemoteAddr(RemoteAddrMode(99)))
	if v, ok := got["remote_addr"]; ok {
		t.Errorf("remote_addr = %v, want it absent for an unknown mode", v)
	}
}
