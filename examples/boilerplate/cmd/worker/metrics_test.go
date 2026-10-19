package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The worker serves /metrics on its own address, from the default registry
// that worker.WithMetrics and the pool collector register on.
func TestStartMetricsServer_ServesMetrics(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, stop, err := startMetricsServer("127.0.0.1:0", logger)
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	t.Cleanup(stop)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + srv.Addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "go_goroutines") {
		t.Fatalf("GET /metrics = %d, body missing go_goroutines", resp.StatusCode)
	}
}

func TestStartMetricsServer_StopShutsItDown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, stop, err := startMetricsServer("127.0.0.1:0", logger)
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+srv.Addr+"/metrics", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("GET /metrics succeeded after stop")
	}
}

func TestStartMetricsServer_BindErrorIsReturned(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, _, err := startMetricsServer("256.0.0.1:0", logger); err == nil {
		t.Fatal("startMetricsServer on an invalid address = nil error, want one")
	}
}
