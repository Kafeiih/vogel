package http

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	vmw "github.com/kafeiih/vogel/httpx/middleware"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/handler"
)

type tracingProbe struct {
	before, after []http.Header
}

func (p *tracingProbe) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.before = append(p.before, w.Header().Clone())
		next.ServeHTTP(w, r)
		p.after = append(p.after, w.Header().Clone())
	})
}

func newTracedRouter(t *testing.T, probe *tracingProbe) http.Handler {
	t.Helper()
	metrics, err := vmw.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(Dependencies{
		Config:        RouterConfig{Timeout: 5 * time.Second},
		Authenticator: denyAll{},
		AuthzChecker:  denyAll{},
		Metrics:       metrics,
		Logger:        logger,
		HealthHandler: handler.NewHealthHandler(okPinger{}, logger),
		Tracing:       probe.middleware,
	})
}

func TestNewRouter_MountsTracingFirst(t *testing.T) {
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/livez", http.StatusOK},
		{"/v1/audit/", http.StatusUnauthorized},
		{"/nope", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			probe := &tracingProbe{}
			rec := get(newTracedRouter(t, probe), tc.path)
			if rec.Code != tc.want {
				t.Fatalf("GET %s = %d, want %d", tc.path, rec.Code, tc.want)
			}
			if len(probe.before) != 1 {
				t.Fatalf("tracing saw %d requests, want 1", len(probe.before))
			}
			if len(probe.before[0]) != 0 {
				t.Errorf("headers already set when tracing was entered: %v", probe.before[0])
			}
			for _, header := range []string{vmw.RequestIDHeader, "X-Content-Type-Options"} {
				if probe.after[0].Get(header) == "" {
					t.Errorf("%s not set inside the tracing middleware", header)
				}
			}
		})
	}
}
