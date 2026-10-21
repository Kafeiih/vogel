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

// vogel:component cerbos begin
// vogel:component cerbos end
var matrixPolicy = map[string]map[string][]string{
	"audit:entry": {
		"list": {"admin"},
		"view": {"admin", "auditor"},
	},
	// vogel:component bpms begin
	"bpms:process": {
		"publish": {"admin"},
		"read":    {"admin", "operator"},
		"start":   {"admin", "operator"},
	},
	"bpms:case": {
		"inbox":  {"admin", "operator"},
		"view":   {"admin", "operator"},
		"claim":  {"admin", "operator"},
		"submit": {"admin", "operator"},
	},
	// vogel:component bpms end
}

func TestNewRouter_RoleMatrix(t *testing.T) {
	A, F, U := allowed, forbidden, unauthenticated
	entryPath := "/audit/" + uuid.NewString()
	// vogel:component bpms begin
	casePath := "/cases/" + uuid.NewString()
	// vogel:component bpms end

	tests := []struct {
		method, route, path string
		want                [5]outcome
	}{
		{"GET", "/healthz", "/healthz", [5]outcome{A, A, A, A, A}},
		{"GET", "/readyz", "/readyz", [5]outcome{A, A, A, A, A}},
		{"GET", "/livez", "/livez", [5]outcome{A, A, A, A, A}},
		{"GET", "/swagger/*", "/swagger/index.html", [5]outcome{A, A, A, A, A}},
		{"GET", "/audit/", "/audit/", [5]outcome{U, U, F, F, A}},
		{"GET", "/audit/{id}", entryPath, [5]outcome{U, U, F, A, A}},
		// vogel:component bpms begin
		{"POST", "/processes", "/processes", [5]outcome{U, U, F, F, A}},
		{"GET", "/processes/{name}", "/processes/consulta", [5]outcome{U, U, F, F, A}},
		{"POST", "/processes/{name}/cases", "/processes/consulta/cases", [5]outcome{U, U, F, F, A}},
		{"GET", "/inbox", "/inbox", [5]outcome{U, U, F, F, A}},
		{"GET", "/cases/{id}", casePath, [5]outcome{U, U, F, F, A}},
		{"POST", "/cases/{id}/claim", casePath + "/claim", [5]outcome{U, U, F, F, A}},
		{"POST", "/cases/{id}/submit", casePath + "/submit", [5]outcome{U, U, F, F, A}},
		// vogel:component bpms end
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
		// vogel:component bpms begin

		ProcessHandler: handler.NewProcessHandler(matrixProcessService{}, logger),
		// vogel:component bpms end
	})
}

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

type matrixChecker struct{}

func (matrixChecker) IsAllowed(_ context.Context, p authz.Principal, r authz.Resource, action string) (bool, error) {
	for _, role := range matrixPolicy[r.Kind][action] {
		if slices.Contains(p.Roles, role) {
			return true, nil
		}
	}
	return false, nil
}

type emptyAuditRepo struct{}

func (emptyAuditRepo) Create(context.Context, pgxtx.DBTX, *audit.Entry) error { return nil }

func (emptyAuditRepo) GetByID(context.Context, uuid.UUID) (*audit.Entry, error) {
	return nil, pgx.ErrNoRows
}

func (emptyAuditRepo) List(context.Context, audit.Filters) ([]audit.Entry, int, error) {
	return nil, 0, nil
}
