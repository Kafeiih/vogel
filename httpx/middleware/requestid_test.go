package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/kafeiih/vogel/reqctx"
)

var requestIDFormat = regexp.MustCompile(`^[0-9a-f]{32}$`)

// serveRequestID runs one request through RequestID and returns the server
// ID and client ID the next handler saw, plus the recorder.
func serveRequestID(t *testing.T, clientHeader string, setHeader bool) (serverID, clientID, chiID string, rec *httptest.ResponseRecorder) {
	t.Helper()
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverID = reqctx.RequestIDFromContext(r.Context())
		clientID = reqctx.ClientRequestIDFromContext(r.Context())
		chiID = chimw.GetReqID(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if setHeader {
		req.Header.Set(RequestIDHeader, clientHeader)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return serverID, clientID, chiID, rec
}

func TestRequestID_GeneratesUniqueWellFormedIDs(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id, _, _, _ := serveRequestID(t, "", false)
		if !requestIDFormat.MatchString(id) {
			t.Fatalf("request ID %q is not 32 lowercase hex characters", id)
		}
		if seen[id] {
			t.Fatalf("request ID %q generated twice", id)
		}
		seen[id] = true
	}
}

func TestRequestID_SetsResponseHeaderAndChiKey(t *testing.T) {
	id, _, chiID, rec := serveRequestID(t, "", false)
	if got := rec.Header().Get(RequestIDHeader); got != id {
		t.Errorf("response %s = %q, want the server ID %q", RequestIDHeader, got, id)
	}
	if chiID != id {
		t.Errorf("chi GetReqID = %q, want the server ID %q (so chi-based readers agree)", chiID, id)
	}
}

func TestRequestID_NeverTrustsIncomingHeader(t *testing.T) {
	id, clientID, chiID, rec := serveRequestID(t, "attacker-chosen-id", true)
	if id == "attacker-chosen-id" || chiID == "attacker-chosen-id" {
		t.Fatalf("server request ID = %q (chi %q), must never be the client-supplied value", id, chiID)
	}
	if !requestIDFormat.MatchString(id) {
		t.Errorf("request ID %q is not a generated ID", id)
	}
	if got := rec.Header().Get(RequestIDHeader); got != id {
		t.Errorf("response %s = %q, want the server ID %q", RequestIDHeader, got, id)
	}
	if clientID != "attacker-chosen-id" {
		t.Errorf("client request ID = %q, want %q kept for correlation", clientID, "attacker-chosen-id")
	}
}

func TestSanitizeClientRequestID(t *testing.T) {
	ctrl := string(rune(0x0a)) + string(rune(0x0d)) + string(rune(0x1b)) + string(rune(0x7f)) + string(rune(0x00))
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty stays empty", in: "", want: ""},
		{name: "visible ASCII kept", in: "abc-123_XYZ.{}", want: "abc-123_XYZ.{}"},
		{name: "control characters stripped", in: "ab" + ctrl + "cd", want: "abcd"},
		{name: "forged log line stripped of newline", in: "id" + string(rune(0x0a)) + "level=ERROR", want: "idlevel=ERROR"},
		{name: "spaces stripped", in: "a b c", want: "abc"},
		{name: "non-ASCII stripped", in: "caf" + string(rune(0xe9)) + "-id", want: "caf-id"},
		{name: "only control characters becomes empty", in: ctrl, want: ""},
		{name: "truncated to the maximum", in: strings.Repeat("x", maxClientRequestIDLen+50), want: strings.Repeat("x", maxClientRequestIDLen)},
		{name: "truncation counts kept characters", in: ctrl + strings.Repeat("y", maxClientRequestIDLen) + "z", want: strings.Repeat("y", maxClientRequestIDLen)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeClientRequestID(tt.in); got != tt.want {
				t.Errorf("sanitizeClientRequestID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	if maxClientRequestIDLen != 128 {
		t.Errorf("maxClientRequestIDLen = %d, want 128", maxClientRequestIDLen)
	}
}

func TestRequestID_SanitizesClientIDInContext(t *testing.T) {
	in := "corr" + string(rune(0x0a)) + string(rune(0x1b)) + "-1" + strings.Repeat("z", 200)
	_, clientID, _, _ := serveRequestID(t, in, true)
	if strings.ContainsAny(clientID, string(rune(0x0a))+string(rune(0x1b))) {
		t.Errorf("client request ID %q still contains control characters", clientID)
	}
	if len(clientID) != maxClientRequestIDLen || !strings.HasPrefix(clientID, "corr-1z") {
		t.Errorf("client request ID = %q (len %d), want sanitized prefix corr-1z... truncated to %d", clientID, len(clientID), maxClientRequestIDLen)
	}
}

func TestRequestID_NoClientHeader_LeavesClientIDEmpty(t *testing.T) {
	_, clientID, _, _ := serveRequestID(t, "", false)
	if clientID != "" {
		t.Errorf("client request ID = %q, want empty without the header", clientID)
	}
}
