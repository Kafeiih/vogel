package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckHeartbeat(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * time.Second

	t.Run("fresh beat passes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "hb")
		if err := writeHeartbeat(path, now.Add(-maxAge+time.Second)); err != nil {
			t.Fatalf("writeHeartbeat: %v", err)
		}
		if err := checkHeartbeat(path, now, maxAge); err != nil {
			t.Errorf("checkHeartbeat = %v, want nil", err)
		}
	})

	t.Run("stale beat fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "hb")
		if err := writeHeartbeat(path, now.Add(-maxAge-time.Second)); err != nil {
			t.Fatalf("writeHeartbeat: %v", err)
		}
		err := checkHeartbeat(path, now, maxAge)
		if err == nil || !strings.Contains(err.Error(), "stale") {
			t.Errorf("checkHeartbeat = %v, want a stale error", err)
		}
	})

	t.Run("missing file fails", func(t *testing.T) {
		err := checkHeartbeat(filepath.Join(t.TempDir(), "hb"), now, maxAge)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("checkHeartbeat = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("unparsable content fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "hb")
		if err := os.WriteFile(path, []byte("not a time\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := checkHeartbeat(path, now, maxAge); err == nil {
			t.Error("checkHeartbeat = nil, want an error for garbage content")
		}
	})
}

func TestRunHeartbeat_BeatsUntilCanceledThenRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hb")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, path, 10*time.Millisecond, logger)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := checkHeartbeat(path, time.Now(), time.Second); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no fresh heartbeat while running")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	<-done
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("heartbeat file still present after stop: %v", err)
	}
}

func TestParseCommand_Healthcheck(t *testing.T) {
	got, err := parseCommand("healthcheck")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != cmdHealthcheck {
		t.Errorf("parseCommand(healthcheck) = %d, want cmdHealthcheck", got)
	}
}
