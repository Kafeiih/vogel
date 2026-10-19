package otel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gootel "go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestSetup_ValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "endpoint without scheme", cfg: Config{Endpoint: "collector:4318", ServiceName: "api"}, want: "endpoint"},
		{name: "endpoint with another scheme", cfg: Config{Endpoint: "grpc://collector:4317", ServiceName: "api"}, want: "endpoint"},
		{name: "endpoint without host", cfg: Config{Endpoint: "http://", ServiceName: "api"}, want: "endpoint"},
		{name: "missing service name", cfg: Config{Endpoint: "http://collector:4318"}, want: "service name"},
		{name: "negative ratio", cfg: Config{Endpoint: "http://collector:4318", ServiceName: "api", SampleRatio: -0.1}, want: "sample ratio"},
		{name: "ratio above one", cfg: Config{Endpoint: "http://collector:4318", ServiceName: "api", SampleRatio: 1.5}, want: "sample ratio"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Setup(context.Background(), tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Setup(%+v) = (%v, %v), want an error about %s", tt.cfg, p, err, tt.want)
			}
		})
	}
}

// Without an endpoint tracing is off: nothing global is installed and
// Shutdown is a no-op.
func TestSetup_NoEndpointIsDisabled(t *testing.T) {
	before := gootel.GetTracerProvider()
	p, err := Setup(context.Background(), Config{ServiceName: "api"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if p.Enabled() {
		t.Error("Enabled() = true without an endpoint")
	}
	if gootel.GetTracerProvider() != before {
		t.Error("Setup without an endpoint replaced the global TracerProvider")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	var nilProvider *Provider
	if err := nilProvider.Shutdown(context.Background()); err != nil {
		t.Errorf("nil Provider Shutdown: %v", err)
	}
}

// With an endpoint, spans are exported over OTLP/HTTP to <endpoint>/v1/traces
// and flushed by Shutdown, and the global provider and propagator are set.
func TestSetup_ExportsOverOTLPHTTP(t *testing.T) {
	var posts atomic.Int32
	var path, contentType atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		path.Store(r.URL.Path)
		contentType.Store(r.Header.Get("Content-Type"))
		posts.Add(1)
	}))
	t.Cleanup(srv.Close)
	restoreGlobals(t)

	p, err := Setup(context.Background(), Config{Endpoint: srv.URL, ServiceName: "api", SampleRatio: 1, Headers: map[string]string{"x-team": "core"}})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !p.Enabled() {
		t.Fatal("Enabled() = false with an endpoint")
	}
	_, span := gootel.Tracer("test").Start(context.Background(), "op")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if posts.Load() == 0 {
		t.Fatal("no OTLP export reached the collector")
	}
	if got := path.Load(); got != "/v1/traces" {
		t.Errorf("export path = %v, want /v1/traces", got)
	}
	if got := contentType.Load(); got != "application/x-protobuf" {
		t.Errorf("content type = %v, want application/x-protobuf", got)
	}
	if fields := gootel.GetTextMapPropagator().Fields(); !slices.Contains(fields, "traceparent") {
		t.Errorf("global propagator fields = %v, want traceparent", fields)
	}
}

func TestSampler_DefaultRatioAndParentBased(t *testing.T) {
	if DefaultSampleRatio != 0.1 {
		t.Errorf("DefaultSampleRatio = %v, want 0.1", DefaultSampleRatio)
	}
	s := sampler(Config{})
	if !strings.Contains(s.Description(), "ParentBased") || !strings.Contains(s.Description(), "0.1") {
		t.Errorf("sampler = %s, want ParentBased(TraceIDRatioBased(0.1))", s.Description())
	}
	if d := sampler(Config{SampleRatio: 1}).Description(); !strings.Contains(d, "TraceIDRatioBased{1}") && !strings.Contains(d, "AlwaysOn") {
		t.Errorf("sampler(1) = %s, want every root sampled", d)
	}
}

func TestTraceIDs(t *testing.T) {
	if tid, sid := TraceIDs(context.Background()); tid != "" || sid != "" {
		t.Errorf("TraceIDs without a span = (%q, %q), want empty", tid, sid)
	}
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	tid, sid := TraceIDs(ctx)
	if tid != span.SpanContext().TraceID().String() || sid != span.SpanContext().SpanID().String() {
		t.Errorf("TraceIDs = (%q, %q), want the span's IDs", tid, sid)
	}
	if len(tid) != 32 || len(sid) != 16 {
		t.Errorf("TraceIDs lengths = (%d, %d), want (32, 16)", len(tid), len(sid))
	}
}

func restoreGlobals(t *testing.T) {
	t.Helper()
	tp, prop := gootel.GetTracerProvider(), gootel.GetTextMapPropagator()
	t.Cleanup(func() {
		gootel.SetTracerProvider(tp)
		gootel.SetTextMapPropagator(prop)
	})
	gootel.SetTracerProvider(noop.NewTracerProvider())
}
