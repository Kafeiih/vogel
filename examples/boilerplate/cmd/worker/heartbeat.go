package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	heartbeatInterval = 10 * time.Second
	heartbeatMaxAge   = 3 * heartbeatInterval
)

func heartbeatPath() string {
	return filepath.Join(os.TempDir(), "vogel-worker.heartbeat")
}

func writeHeartbeat(path string, now time.Time) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(now.UTC().Format(time.RFC3339Nano) + "\n")
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

func checkHeartbeat(path string, now time.Time, maxAge time.Duration) error {
	data, err := os.ReadFile(path) //nolint:gosec // path is heartbeatPath() (a fixed name in the temp directory), never input
	if err != nil {
		return fmt.Errorf("reading heartbeat: %w", err)
	}
	beat, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("parsing heartbeat: %w", err)
	}
	if age := now.Sub(beat); age > maxAge {
		return fmt.Errorf("heartbeat stale: last beat %s ago, limit %s", age.Round(time.Second), maxAge)
	}
	return nil
}

func runHeartbeat(ctx context.Context, path string, interval time.Duration, logger *slog.Logger) {
	defer func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logger.Warn("removing heartbeat file", "path", path, "error", err)
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := writeHeartbeat(path, time.Now()); err != nil {
			logger.Warn("writing heartbeat file", "path", path, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
