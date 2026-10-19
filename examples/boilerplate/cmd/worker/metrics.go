package main

import (
	"log/slog"
	"net/http"

	httpiface "github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http"
)

// startMetricsServer binds addr and serves /metrics there, like the api's
// metrics listener, from the default registry worker.WithMetrics and the pool
// collector register on. stop shuts the server down; a later unexpected stop
// is logged by WatchMetricsErrors. Binding fails before the worker starts
// processing jobs, so a bad WORKER_METRICS_LISTEN_ADDR fails the boot.
func startMetricsServer(addr string, logger *slog.Logger) (srv *http.Server, stop func(), err error) {
	srv = httpiface.NewMetricsServer(addr, logger)
	errCh, err := httpiface.StartMetricsServer(srv, logger)
	if err != nil {
		return nil, nil, err
	}
	go httpiface.WatchMetricsErrors(errCh, logger)
	return srv, httpiface.MetricsDrainer(srv, logger), nil
}
