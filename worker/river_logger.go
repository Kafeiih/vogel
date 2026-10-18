package worker

import (
	"context"
	"log/slog"
)

// riverLogLevel is the lowest level River logs at through the app's logger.
// River's own default logger starts at warn; River is chatty below it (job
// fetches, leadership, queue maintenance), so passing the app's logger in
// must not lower that floor.
const riverLogLevel = slog.LevelWarn

// riverLogger returns logger with records below riverLogLevel dropped, or nil
// (River's default) for a nil logger. The app's handler still decides above
// that floor, so an app logging at error only stays at error.
func riverLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return nil
	}
	return slog.New(&minLevelHandler{inner: logger.Handler(), min: riverLogLevel})
}

// minLevelHandler drops records below min and hands the rest to inner.
type minLevelHandler struct {
	inner slog.Handler
	min   slog.Level
}

func (h *minLevelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.min && h.inner.Enabled(ctx, level)
}

func (h *minLevelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}

func (h *minLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &minLevelHandler{inner: h.inner.WithAttrs(attrs), min: h.min}
}

func (h *minLevelHandler) WithGroup(name string) slog.Handler {
	return &minLevelHandler{inner: h.inner.WithGroup(name), min: h.min}
}
