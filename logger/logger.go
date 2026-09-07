// Package logger provides a thin, dependency-free wrapper around log/slog.
//
// It does not import an HTTP router or any web framework: a request/correlation
// ID is read from vogel/reqctx, a small dependency-free package that owns the
// context key for it. Bridging a specific router's request ID (e.g. chi's)
// into that context key is the responsibility of the HTTP middleware layer,
// which legitimately knows about the router — see httpx/middleware.
//
// reqctx (rather than this package) owns the key so that the request ID
// reaching a log line and the one reaching an audit_log row (see
// vogel/audit) are provably the same value: both read it from the same
// unexported key in the same package.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/kafeiih/vogel/reqctx"
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
// via reqctx.WithRequestID, if any. If no request ID is present, the empty
// string is used, matching the behavior of an absent ID.
func (l *Logger) WithContext(ctx context.Context) *Logger {
	return &Logger{
		Logger: l.Logger.With("request_id", reqctx.RequestIDFromContext(ctx)),
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
