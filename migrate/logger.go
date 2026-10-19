package migrate

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// slogAdapter adapts log/slog to the goose.Logger interface.
// goose.Logger requires Printf (informational) and Fatalf (fatal — exit 1).
//
// Each line keeps goose's format string as the message and its values under
// "args", instead of one pre-formatted string, so the message stays constant
// for log queries and the values remain structured.
type slogAdapter struct {
	logger *slog.Logger
}

// Printf logs an informational message from goose.
func (a *slogAdapter) Printf(format string, v ...any) {
	a.logger.Info(gooseMessage(format), gooseArgs(v)...)
}

// Fatalf logs an error from goose and exits the process with code 1.
// This mirrors the stdlib log.Fatalf behaviour that goose's default logger uses.
func (a *slogAdapter) Fatalf(format string, v ...any) {
	a.logger.Error(gooseMessage(format), gooseArgs(v)...)
	os.Exit(1)
}

// newSlogAdapter wraps a *slog.Logger as a goose.Logger adapter.
// Pass slog.Default() for production use or slog.New(slog.NewTextHandler(io.Discard, nil))
// for test usage where goose output should be suppressed.
func newSlogAdapter(l *slog.Logger) *slogAdapter {
	return &slogAdapter{logger: l}
}

// gooseMessage returns goose's format string without its trailing newline.
func gooseMessage(format string) string {
	return strings.TrimRight(format, "\n")
}

// gooseArgs returns the "args" attribute for goose's values, or none. An
// error or fmt.Stringer (a time.Duration, for instance) is rendered as its
// text, which a JSON handler would otherwise encode as an opaque object or a
// bare number.
func gooseArgs(v []any) []any {
	if len(v) == 0 {
		return nil
	}
	args := make([]any, len(v))
	for i, x := range v {
		switch x := x.(type) {
		case error:
			args[i] = x.Error()
		case fmt.Stringer:
			args[i] = x.String()
		default:
			args[i] = x
		}
	}
	return []any{"args", args}
}
