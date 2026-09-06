package migrate

import (
	"fmt"
	"log/slog"
	"os"
)

// slogAdapter adapts log/slog to the goose.Logger interface.
// goose.Logger requires Printf (informational) and Fatalf (fatal — exit 1).
type slogAdapter struct {
	logger *slog.Logger
}

// Printf logs an informational message from goose.
func (a *slogAdapter) Printf(format string, v ...any) {
	a.logger.Info(fmt.Sprintf(format, v...))
}

// Fatalf logs an error from goose and exits the process with code 1.
// This mirrors the stdlib log.Fatalf behaviour that goose's default logger uses.
func (a *slogAdapter) Fatalf(format string, v ...any) {
	a.logger.Error(fmt.Sprintf(format, v...))
	os.Exit(1)
}

// newSlogAdapter wraps a *slog.Logger as a goose.Logger adapter.
// Pass slog.Default() for production use or slog.New(slog.NewTextHandler(io.Discard, nil))
// for test usage where goose output should be suppressed.
func newSlogAdapter(l *slog.Logger) *slogAdapter {
	return &slogAdapter{logger: l}
}
