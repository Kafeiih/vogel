// Package logger provides a thin, dependency-free wrapper around log/slog.
//
// It does not import an HTTP router or any web framework: a request/correlation
// ID is passed in as a plain string (via WithRequestID/RequestIDFromContext),
// which the logger stores and reads using a context key it owns. Bridging a
// specific router's request ID (e.g. chi's) into that context key is the
// responsibility of the HTTP middleware layer, which legitimately knows about
// the router — see httpx/middleware.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
)

// Logger wraps *slog.Logger.
type Logger struct {
	*slog.Logger
}

// New creates a Logger writing to w. Log level and format are derived from env:
// "production" and "staging" use LevelInfo, anything else uses LevelDebug.
// "production" emits JSON; other environments emit human-readable text.
func New(env string, w io.Writer) *Logger {
	var handler slog.Handler

	opts := &slog.HandlerOptions{
		Level: getLogLevel(env),
	}

	if env == "production" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return &Logger{
		Logger: slog.New(handler),
	}
}

// NewDefault creates a Logger writing to os.Stdout for the given env.
func NewDefault(env string) *Logger {
	return New(env, os.Stdout)
}

// With returns a Logger that includes the given attributes on every record.
func (l *Logger) With(args ...any) *Logger {
	return &Logger{
		Logger: l.Logger.With(args...),
	}
}

// WithContext returns a Logger annotated with the request ID stored in ctx
// via WithRequestID, if any. If no request ID is present, the empty string
// is used, matching the behavior of an absent ID.
func (l *Logger) WithContext(ctx context.Context) *Logger {
	return &Logger{
		Logger: l.Logger.With("request_id", RequestIDFromContext(ctx)),
	}
}

func getLogLevel(env string) slog.Level {
	switch env {
	case "production":
		return slog.LevelInfo
	case "staging":
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}

// requestIDKey is the context key this package owns for storing a
// request/correlation ID. It is unexported so only WithRequestID may set it.
type requestIDKey struct{}

// WithRequestID returns a copy of ctx carrying id as the request/correlation ID.
// Callers that bridge a specific transport's request ID (e.g. an HTTP
// middleware reading chi's request ID) should call this to make the ID
// available to Logger.WithContext without this package depending on that
// transport.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the request/correlation ID previously stored
// via WithRequestID, or "" if none is present.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
