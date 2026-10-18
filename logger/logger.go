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
//
// Every record's timestamp is emitted in UTC regardless of the host's time
// zone (see utcTime), so log lines from services running in different zones
// can be ordered and correlated without offset arithmetic.
//
// The *Context methods add request_id and user_id from reqctx (see
// contextHandler), so InfoContext(ctx, ...) inside a request is correlated
// without WithContext. Install the Logger with slog.SetDefault(l.Logger) to
// extend that to package-level slog calls and to libraries logging through
// slog.Default().
func New(env string, w io.Writer) *Logger {
	var handler slog.Handler

	opts := &slog.HandlerOptions{
		Level:       getLogLevel(env),
		ReplaceAttr: utcTime,
	}

	if env == "production" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return &Logger{
		Logger: slog.New(&contextHandler{inner: handler}),
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
//
// When ctx carries an authenticated user's ID (reqctx.UserIDFromContext,
// filled by httpx/middleware.Authenticate), it is added as "user_id"; the key
// is left out otherwise, so a line logged outside an authenticated request
// keeps its shape.
func (l *Logger) WithContext(ctx context.Context) *Logger {
	args := []any{requestIDKey, reqctx.RequestIDFromContext(ctx)}
	if userID := reqctx.UserIDFromContext(ctx); userID != "" {
		args = append(args, userIDKey, userID)
	}
	return &Logger{
		Logger: l.Logger.With(args...),
	}
}

// utcTime is a slog.HandlerOptions.ReplaceAttr that converts the record's
// built-in timestamp to UTC. It touches only the top-level slog.TimeKey
// attribute (groups is empty for it): a time.Time the caller logs as an
// ordinary attribute under any other key, or inside a group, keeps the zone
// it was given. A caller attribute literally keyed "time" at the top level is
// indistinguishable from the built-in one here and is converted too.
func utcTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		a.Value = slog.TimeValue(a.Value.Time().UTC())
	}
	return a
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
