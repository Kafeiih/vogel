package http

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

func NewServer(addr string, handler http.Handler, logger *slog.Logger) *Server {
	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadTimeout:       15 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		logger: logger,
	}
}

// Start runs the HTTP server and blocks until a shutdown signal is received.
// drainers are called after http.Server.Shutdown completes, before the process exits.
// Use this to wait for background goroutines to finish.
func (s *Server) Start(drainers ...func()) error {
	serverErrors := make(chan error, 1)

	go func() {
		s.logger.Info("starting http server", "addr", s.httpServer.Addr)
		serverErrors <- s.httpServer.ListenAndServe()
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		return fmt.Errorf("server error: %w", err)

	case sig := <-shutdown:
		s.logger.Info("shutdown signal received", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := s.httpServer.Shutdown(ctx); err != nil {
			s.logger.Error("graceful shutdown failed, forcing", "error", err)
			if err := s.httpServer.Close(); err != nil {
				return fmt.Errorf("could not close server: %w", err)
			}
		}

		s.logger.Info("server stopped gracefully")

		for _, drain := range drainers {
			drain()
		}
	}

	return nil
}
