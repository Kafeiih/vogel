package otel

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	gootel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/kafeiih/vogel/notification"
)

const instrumentationName = "github.com/kafeiih/vogel/otel"

// SampledTraceIDs is TraceIDs for a sampled span only: it returns empty
// strings for a span that will not be exported. Use it where an ID must
// point at a trace the backend has, such as the exemplars of
// middleware.WithExemplars and postgres.Config.TraceIDs.
func SampledTraceIDs(ctx context.Context) (traceID, spanID string) {
	if !trace.SpanContextFromContext(ctx).IsSampled() {
		return "", ""
	}
	return TraceIDs(ctx)
}

// Transport returns a wrapper for the WrapTransport field of vogel's
// outbound clients (auth/zitadel, notification/sendgrid, storage/s3): each
// request gets a client span, a child of the caller's, and carries its
// traceparent. Baggage is not sent, since those clients talk to third
// parties. The span is named after the method only: the URL path holds IDs.
// WithTracerProvider applies; WithTrustedPropagation does not.
func Transport(opts ...MiddlewareOption) func(http.RoundTripper) http.RoundTripper {
	cfg := middlewareConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	topts := []otelhttp.Option{otelhttp.WithPropagators(propagation.TraceContext{})}
	if cfg.tp != nil {
		topts = append(topts, otelhttp.WithTracerProvider(cfg.tp))
	}
	return func(base http.RoundTripper) http.RoundTripper {
		return otelhttp.NewTransport(base, topts...)
	}
}

// Notifier wraps n so every Send records a client span named
// notification.send. The span carries no recipient, sender or subject, and a
// failed Send marks it as an error without the error text: those are
// personal data, and an SMTP rejection often quotes the address. It is the
// span for notification/smtp, which makes no HTTP call; for
// notification/sendgrid it encloses the HTTP span Transport records.
// WithTracerProvider applies; WithTrustedPropagation does not.
func Notifier(n notification.Notifier, opts ...MiddlewareOption) notification.Notifier {
	cfg := middlewareConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &tracedNotifier{next: n, tp: cfg.tp}
}

type tracedNotifier struct {
	next notification.Notifier
	tp   trace.TracerProvider // nil: the global one, read on every span
}

func (t *tracedNotifier) Send(ctx context.Context, msg *notification.Message) error {
	tp := t.tp
	if tp == nil {
		tp = gootel.GetTracerProvider()
	}
	ctx, span := tp.Tracer(instrumentationName).Start(ctx, "notification.send", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	err := t.next.Send(ctx, msg)
	if err != nil {
		span.SetStatus(codes.Error, "send failed")
	}
	return err
}
