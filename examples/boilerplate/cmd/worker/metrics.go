package main

import (
	"log/slog"
	"net/http"

	httpiface "github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http"
)

func startMetricsServer(addr string, logger *slog.Logger) (srv *http.Server, stop func(), err error) {
	srv = httpiface.NewMetricsServer(addr, logger)
	errCh, err := httpiface.StartMetricsServer(srv, logger)
	if err != nil {
		return nil, nil, err
	}
	go httpiface.WatchMetricsErrors(errCh, logger)
	return srv, httpiface.MetricsDrainer(srv, logger), nil
}
