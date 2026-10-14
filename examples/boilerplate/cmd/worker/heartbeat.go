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

// The worker serves no port, so its container healthcheck cannot probe an
// endpoint the way the api's does. Instead "worker server" rewrites a
// heartbeat file while its job queue runs, and "worker healthcheck" (the
// image's HEALTHCHECK, a second process in the same container) fails when
// the file is missing or older than heartbeatMaxAge.
//
// A fresh beat proves the process is alive, its job queue was started and
// shutdown has not begun. It does not prove the database is reachable: a
// restart would not fix an outage there, and River already logs it.
const (
	heartbeatInterval = 10 * time.Second
	heartbeatMaxAge   = 3 * heartbeatInterval
)

// heartbeatPath is the heartbeat file, in the temp directory because the
// deploy compose runs the worker with a read-only root filesystem and a
// tmpfs on /tmp.
func heartbeatPath() string {
	return filepath.Join(os.TempDir(), "vogel-worker.heartbeat")
}

// writeHeartbeat records now in path. It writes a sibling file and renames
// it, so checkHeartbeat never reads a half-written timestamp.
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

// checkHeartbeat returns nil when path holds a beat no older than maxAge at
// now. A missing file is reported as an error wrapping os.ErrNotExist.
func checkHeartbeat(path string, now time.Time, maxAge time.Duration) error {
	data, err := os.ReadFile(path)
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

// runHeartbeat writes a beat at once and then every interval until ctx is
// done, and removes the file on return so a stopping worker reads as
// unhealthy. A failed write is logged; the next tick retries it.
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
