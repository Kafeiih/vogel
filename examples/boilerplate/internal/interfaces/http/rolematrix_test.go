package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kafeiih/vogel/audit"
	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/authz"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/pgxtx"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/handler"
)

// outcome is what the router's guards decide for one request: rejected by
// Authenticate (401), rejected by RequirePermission (403), or let through to
// the handler.
type outcome int

const (
	unauthenticated outcome = iota
	forbidden
	allowed
)

func (o outcome) String() string {
	return [...]string{"401", "403", "allowed"}[o]
}

func outcomeOf(code int) outcome {
	switch code {
	case http.StatusUnauthorized:
		return unauthenticated
	case http.StatusForbidden:
		return forbidden
	default:
		return allowed
	}
}

// matrixCallers are matrixAuthenticator tokens ("<userID>:<roles>"); an empty
// token sends no Authorization header at all.
var matrixCallers = []struct {
	name  string
	token string
}{
	{"anonymous", ""},
	{"malformed token", "not-a-token"},
	{"no role", "u1:"},
	{"auditor", "u2:auditor"},
	{"admin", "u3:admin"},
}

// matrixPolicy stands in for the Cerbos policies the PDP serves: the roles
// allowed each action on each resource kind. Keep the two in sync.
// vogel:component cerbos begin
// The bundled policies are in cerbos/policies, whose own tests run with make
// cerbos-test.
// vogel:component cerbos end
// It is deliberately asymmetric (an
// auditor may view an entry but not list them) so a route wired to the wrong
// action shows up as a wrong cell.
var matrixPolicy = map[string]map[string][]string{
	"audit:entry": {
		"list": {"admin"},
		"view": {"admin", "auditor"},
	},
}

// TestNewRouter_RoleMatrix pins, for every route and every kind of caller,
// whether the router's guards let the request through. A change to a
// route's Authenticate group or to the resource/action it asks the checker
// about shows up here as a named route x caller cell, not as a silent
// widening of access.
func TestNewRouter_RoleMatrix(t *testing.T) {
	A, F, U := allowed, forbidden, unauthenticated
	entryPath := "/v1/audit/" + uuid.NewString()

	// Columns follow matrixCallers: anonymous, malformed, no role, auditor, admin.
	tests := []struct {
		method, route, path string
		want                [5]outcome
	}{
		// Public probes: Kubernetes calls them without credentials.
		{"GET", "/v1/healthz", "/v1/healthz", [5]outcome{A, A, A, A, A}},
		{"GET", "/v1/readyz", "/v1/readyz", [5]outcome{A, A, A, A, A}},
		{"GET", "/v1/livez", "/v1/livez", [5]outcome{A, A, A, A, A}},
		// Only registered with SWAGGER_ENABLED=true, which Load refuses in
		// production; public when it is on.
		{"GET", "/v1/swagger/*", "/v1/swagger/index.html", [5]outcome{A, A, A, A, A}},
		{"GET", "/v1/audit/", "/v1/audit/", [5]outcome{U, U, F, F, A}},
		{"GET", "/v1/audit/{id}", entryPath, [5]outcome{U, U, F, A, A}},
	}

	router := newMatrixRouter(t)

	covered := map[string]bool{}
	for _, tt := range tests {
		covered[tt.method+" "+tt.route] = true
		for i, caller := range matrixCallers {
			t.Run(tt.method+" "+tt.path+"/"+caller.name, func(t *testing.T) {
				req := httptest.NewRequest(tt.method, tt.path, nil)
				req.RemoteAddr = "203.0.113.9:1234"
				if caller.token != "" {
					req.Header.Set("Authorization", "Bearer "+caller.token)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				if got := outcomeOf(rec.Code); got != tt.want[i] {
					t.Errorf("%s %s as %s = %d (%s), want %s",
						tt.method, tt.path, caller.name, rec.Code, got, tt.want[i])
				}
			})
		}
	}

	// Every registered route must have a row, so a new route cannot ship
	// without someone deciding who may call it.
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if key := method + " " + route; !covered[key] {
			t.Errorf("%s has no row in the role matrix", key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
}

// newMatrixRouter wires NewRouter as main.go does, with every optional route
// registered (Swagger on) and the auth ports replaced by matrixAuthenticator
// and matrixChecker.
func newMatrixRouter(t *testing.T) *chi.Mux {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics, err := vmw.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	return NewRouter(Dependencies{
		Config:        RouterConfig{Timeout: 5 * time.Second, EnableSwagger: true},
		Authenticator: matrixAuthenticator{},
		AuthzChecker:  matrixChecker{},
		Metrics:       metrics,
		Logger:        logger,
		HealthHandler: handler.NewHealthHandler(okPinger{}, logger),
		AuditHandler:  handler.NewAuditHandler(emptyAuditRepo{}, logger),
	})
}

// matrixAuthenticator accepts "<userID>:<comma-separated roles>" tokens and
// rejects anything else.
type matrixAuthenticator struct{}

func (matrixAuthenticator) Authenticate(_ context.Context, token string) (*auth.Principal, error) {
	userID, roles, ok := strings.Cut(token, ":")
	if !ok || userID == "" {
		return nil, auth.ErrUnauthenticated
	}
	p := &auth.Principal{UserID: userID, Username: userID}
	if roles != "" {
		p.Roles = strings.Split(roles, ",")
	}
	return p, nil
}

// matrixChecker allows an action when the principal holds one of the roles
// matrixPolicy lists for it.
type matrixChecker struct{}

func (matrixChecker) IsAllowed(_ context.Context, p authz.Principal, r authz.Resource, action string) (bool, error) {
	for _, role := range matrixPolicy[r.Kind][action] {
		if slices.Contains(p.Roles, role) {
			return true, nil
		}
	}
	return false, nil
}

// emptyAuditRepo answers every read with no rows, so an allowed audit
// request ends in 200 (list) or 404 (entry), never in 401 or 403.
type emptyAuditRepo struct{}

func (emptyAuditRepo) Create(context.Context, pgxtx.DBTX, *audit.Entry) error { return nil }

func (emptyAuditRepo) GetByID(context.Context, uuid.UUID) (*audit.Entry, error) {
	return nil, pgx.ErrNoRows
}

func (emptyAuditRepo) List(context.Context, audit.Filters) ([]audit.Entry, int, error) {
	return nil, 0, nil
}
