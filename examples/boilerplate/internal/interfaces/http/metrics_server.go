package http

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewMetricsServer creates an http.Server that exposes only /metrics.
// Timeouts mirror the main server (see NewServer).
func NewMetricsServer(addr string, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// StartMetricsServer binds the listener and starts the metrics server in a goroutine.
// Returns a channel that receives exactly one value: nil on clean shutdown, non-nil on error.
func StartMetricsServer(srv *http.Server, logger *slog.Logger) (<-chan error, error) {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, err
	}

	// Resolve the actual bound address (useful when addr is ":0" in tests).
	srv.Addr = ln.Addr().String()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting metrics server", "addr", srv.Addr)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	return errCh, nil
}

// WatchMetricsErrors blocks until the metrics server stops and logs unexpected errors.
// Call this in a goroutine alongside the main server.
func WatchMetricsErrors(errCh <-chan error, logger *slog.Logger) {
	if err := <-errCh; err != nil {
		logger.Error("metrics server stopped unexpectedly",
			"component", "metrics-server",
			"error", err,
			"remediation", "redeploy or restart the service to restore metrics collection",
		)
	}
}

// MetricsDrainer returns a drainer function that gracefully shuts down the metrics server.
// Register it with server.Start(...) so shutdown propagates on SIGTERM/SIGINT.
func MetricsDrainer(srv *http.Server, logger *slog.Logger) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("metrics server shutdown error", "error", err)
		}
	}
}
