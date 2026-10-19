// Package otel is vogel's OpenTelemetry adapter: it exports traces over
// OTLP/HTTP (Setup), traces incoming HTTP requests (Middleware), and reads
// the active span's IDs for logger.WithTraceIDs (TraceIDs). The other ports
// vogel exposes for tracing (postgres.Config.Tracer, worker.WithMiddleware,
// the WrapTransport and StatsHandler fields of the outbound clients) take
// any implementation, e.g. otelhttp.NewTransport and
// otelgrpc.NewClientHandler.
//
// Tracing is opt-in at every level: a service that does not import this
// package links no OpenTelemetry SDK, and Setup without an endpoint installs
// nothing, leaving the global tracer a no-op.
//
// Like the rest of vogel it reads no environment variable: Setup takes an
// explicit Config and passes every exporter, batching and span-limit setting
// the SDK would otherwise take from OTEL_* variables, so those variables
// change nothing. The caller maps its own configuration (the standard OTEL_*
// names, for instance) into Config. HTTPS_PROXY and the other proxy
// variables of net/http still apply to the export connection.
package otel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"time"

	gootel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// DefaultSampleRatio is the fraction of new root traces sampled when
// Config.SampleRatio is zero.
const DefaultSampleRatio = 0.1

// Config configures Setup.
type Config struct {
	// Endpoint is the OTLP/HTTP collector base URL, e.g.
	// "http://otel-collector:4318"; spans are sent to its /v1/traces unless
	// the URL has a path. http:// sends in cleartext, https:// uses TLS.
	// Empty disables tracing.
	Endpoint string

	// ServiceName is the service.name resource attribute. Required with an
	// Endpoint.
	ServiceName string
	// ServiceVersion and Environment set service.version and
	// deployment.environment.name when non-empty.
	ServiceVersion string
	Environment    string

	// SampleRatio is the fraction of new root traces that are sampled, in
	// [0, 1]; zero means DefaultSampleRatio. A span with a parent follows
	// its parent's decision (ParentBased), so a trace is kept or dropped
	// whole. Since the HTTP middleware does not adopt a client's
	// traceparent by default, this ratio, not the client, decides.
	SampleRatio float64

	// Headers are sent with every export, e.g. a collector API key. They
	// require an https:// Endpoint, so a credential never travels in
	// cleartext.
	Headers map[string]string
}

// Provider is the result of Setup. Shut it down last, after the HTTP server
// and the job queue, so their final spans are exported.
type Provider struct {
	tp *sdktrace.TracerProvider // nil when tracing is disabled
}

// Setup validates cfg and, when cfg.Endpoint is set, installs a
// TracerProvider exporting to it as the global provider, and W3C Trace
// Context and Baggage as the global propagator. Without an endpoint it
// returns a disabled Provider and changes nothing.
func Setup(ctx context.Context, cfg Config) (*Provider, error) {
	// Written as a negated range check so NaN, for which every comparison is
	// false, is rejected too.
	if !(cfg.SampleRatio >= 0 && cfg.SampleRatio <= 1) {
		return nil, fmt.Errorf("otel: sample ratio must be within [0, 1], got %v", cfg.SampleRatio)
	}
	if cfg.Endpoint == "" {
		return &Provider{}, nil
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("otel: endpoint must be an http:// or https:// URL with a host, got %q", cfg.Endpoint)
	}
	if cfg.ServiceName == "" {
		return nil, errors.New("otel: service name is required with an endpoint")
	}
	if len(cfg.Headers) > 0 && u.Scheme != "https" {
		return nil, errors.New("otel: headers require an https:// endpoint")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/traces"
	}
	headers := cfg.Headers
	if headers == nil {
		headers = map[string]string{} // explicit: OTEL_EXPORTER_OTLP_HEADERS must not apply
	}
	// Every option the SDK would otherwise read from OTEL_* is passed
	// explicitly, with the SDK's own defaults.
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(u.String()),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithCompression(otlptracehttp.NoCompression),
		otlptracehttp.WithTimeout(exportTimeout),
	}
	if u.Scheme == "https" {
		opts = append(opts, otlptracehttp.WithTLSClientConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otel: creating the OTLP exporter: %w", err)
	}

	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentNameKey.String(cfg.Environment))
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(sdktrace.DefaultScheduleDelay*time.Millisecond),
			sdktrace.WithExportTimeout(sdktrace.DefaultExportTimeout*time.Millisecond),
			sdktrace.WithMaxQueueSize(sdktrace.DefaultMaxQueueSize),
			sdktrace.WithMaxExportBatchSize(sdktrace.DefaultMaxExportBatchSize),
		),
		sdktrace.WithRawSpanLimits(spanLimits()),
		sdktrace.WithSampler(sampler(cfg)),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, attrs...)),
	)
	gootel.SetTracerProvider(tp)
	gootel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return &Provider{tp: tp}, nil
}

// Enabled reports whether Setup installed a TracerProvider.
func (p *Provider) Enabled() bool { return p != nil && p.tp != nil }

// Shutdown flushes the spans still buffered and stops the exporter, bounded
// by ctx. It is a no-op on a disabled or nil Provider.
func (p *Provider) Shutdown(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	if err := p.tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("otel: shutting down the tracer provider: %w", err)
	}
	return nil
}

// exportTimeout bounds each export request, the SDK's default.
const exportTimeout = 10 * time.Second

// spanLimits returns the SDK's default span limits without reading the
// OTEL_SPAN_* and OTEL_*_COUNT_LIMIT variables sdktrace.NewSpanLimits reads.
func spanLimits() sdktrace.SpanLimits {
	return sdktrace.SpanLimits{
		AttributeValueLengthLimit:   sdktrace.DefaultAttributeValueLengthLimit,
		AttributeValueDepthLimit:    sdktrace.DefaultAttributeValueDepthLimit,
		AttributeCountLimit:         sdktrace.DefaultAttributeCountLimit,
		EventCountLimit:             sdktrace.DefaultEventCountLimit,
		LinkCountLimit:              sdktrace.DefaultLinkCountLimit,
		AttributePerEventCountLimit: sdktrace.DefaultAttributePerEventCountLimit,
		AttributePerLinkCountLimit:  sdktrace.DefaultAttributePerLinkCountLimit,
	}
}

// sampler returns ParentBased(TraceIDRatioBased(ratio)) for cfg.
func sampler(cfg Config) sdktrace.Sampler {
	ratio := cfg.SampleRatio
	if ratio == 0 {
		ratio = DefaultSampleRatio
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// TraceIDs returns the trace and span IDs of the span in ctx, hex encoded,
// or empty strings without a valid span. It is a logger.TraceIDsFunc:
// logger.New(env, w, logger.WithTraceIDs(otel.TraceIDs)).
func TraceIDs(ctx context.Context) (traceID, spanID string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}
