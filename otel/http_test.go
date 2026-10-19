package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const clientTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

func newRecorder(t *testing.T) (*tracetest.SpanRecorder, trace.TracerProvider) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return rec, tp
}

func serve(t *testing.T, mw func(http.Handler) http.Handler, req *http.Request, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Use(mw)
	r.Get("/items/{id}", handler)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// By default the client's traceparent is untrusted: the request starts a new
// trace, linked to the client's span, so the client controls neither the
// trace ID nor the sampling decision.
func TestMiddleware_UntrustedTraceparentStartsANewLinkedRoot(t *testing.T) {
	rec, tp := newRecorder(t)
	req := httptest.NewRequest(http.MethodGet, "/items/42", nil)
	req.Header.Set("traceparent", clientTraceparent)
	req.Header.Set("baggage", "user=alice")
	var baggageSeen bool

	serve(t, Middleware(WithTracerProvider(tp)), req, func(w http.ResponseWriter, r *http.Request) {
		baggageSeen = baggageMember(r.Context(), "user") != ""
	})

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	s := spans[0]
	if s.SpanContext().TraceID().String() == "0af7651916cd43dd8448eb211c80319c" || s.Parent().IsValid() {
		t.Errorf("span continued the client's trace (parent %v)", s.Parent())
	}
	if len(s.Links()) != 1 || s.Links()[0].SpanContext.TraceID().String() != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("links = %v, want one link to the client's span", s.Links())
	}
	if baggageSeen {
		t.Error("the client's baggage reached the handler")
	}
}

// WithTrustedPropagation continues the caller's trace (behind a trusted
// gateway or mesh).
func TestMiddleware_TrustedPropagationContinuesTheTrace(t *testing.T) {
	rec, tp := newRecorder(t)
	req := httptest.NewRequest(http.MethodGet, "/items/42", nil)
	req.Header.Set("traceparent", clientTraceparent)

	serve(t, Middleware(WithTracerProvider(tp), WithTrustedPropagation()), req, func(http.ResponseWriter, *http.Request) {})

	s := rec.Ended()[0]
	if s.SpanContext().TraceID().String() != "0af7651916cd43dd8448eb211c80319c" || s.Parent().SpanID().String() != "b7ad6b7169203331" {
		t.Errorf("span = trace %s parent %s, want the client's trace and span as parent", s.SpanContext().TraceID(), s.Parent().SpanID())
	}
}

// The span is named after the chi route pattern, not the raw path, and
// carries http.route and the status code.
func TestMiddleware_NamesTheSpanAfterTheRoute(t *testing.T) {
	rec, tp := newRecorder(t)
	req := httptest.NewRequest(http.MethodGet, "/items/42", nil)

	serve(t, Middleware(WithTracerProvider(tp)), req, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	s := rec.Ended()[0]
	if s.Name() != "GET /items/{id}" {
		t.Errorf("span name = %q, want %q", s.Name(), "GET /items/{id}")
	}
	if v := attr(s.Attributes(), "http.route"); v != "/items/{id}" {
		t.Errorf("http.route = %q, want /items/{id}", v)
	}
	if v := attr(s.Attributes(), "http.response.status_code"); v != "418" {
		t.Errorf("http.response.status_code = %q, want 418", v)
	}
}

// An unmatched route keeps a low-cardinality name: the method only.
func TestMiddleware_UnmatchedRouteKeepsTheMethodName(t *testing.T) {
	rec, tp := newRecorder(t)
	serve(t, Middleware(WithTracerProvider(tp)), httptest.NewRequest(http.MethodGet, "/no/such/path", nil), func(http.ResponseWriter, *http.Request) {})

	s := rec.Ended()[0]
	if s.Name() != "GET" {
		t.Errorf("span name = %q, want GET", s.Name())
	}
	if v := attr(s.Attributes(), "http.route"); v != "" {
		t.Errorf("http.route = %q, want it absent", v)
	}
}

// The handler sees the server span in its context, so TraceIDs (and a logger
// with WithTraceIDs) report it.
func TestMiddleware_HandlerSeesTheSpan(t *testing.T) {
	rec, tp := newRecorder(t)
	var traceID string
	serve(t, Middleware(WithTracerProvider(tp)), httptest.NewRequest(http.MethodGet, "/items/1", nil), func(_ http.ResponseWriter, r *http.Request) {
		traceID, _ = TraceIDs(r.Context())
	})
	if traceID == "" || traceID != rec.Ended()[0].SpanContext().TraceID().String() {
		t.Errorf("TraceIDs in the handler = %q, want the server span's trace ID", traceID)
	}
}

func attr(kvs []attribute.KeyValue, key string) string {
	for _, kv := range kvs {
		if string(kv.Key) == key {
			return kv.Value.String()
		}
	}
	return ""
}

func baggageMember(ctx context.Context, key string) string {
	return baggage.FromContext(ctx).Member(key).Value()
}
