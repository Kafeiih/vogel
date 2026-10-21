// Command bpms is a runnable example of a process model on top of vogel's
// workflow engine: a citizen inquiry form ("consulta") whose definition is
// versioned JSON, whose fields come from a plugin registry with CEL rules,
// and whose handler is a profile resolved live, never a fixed person.
//
// The engine stays a state machine: forms, values and publishing live in
// this example, as the workflow package asks of its consumers.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kafeiih/vogel/logger"
	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/postgres"
	"github.com/kafeiih/vogel/workflow"
	wfmigrations "github.com/kafeiih/vogel/workflow/migrations"
	wfpg "github.com/kafeiih/vogel/workflow/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log := logger.New("development", os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, postgres.Config{URL: dbURL, MaxConns: 10, MinConns: 2}, log.Logger)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	// This example's tables and the engine's, each with its own goose
	// version table (see examples/api for why they must not share one).
	appFS, err := appMigrationsFS()
	if err != nil {
		return fmt.Errorf("app migrations fs: %w", err)
	}
	if err := migrate.Up(ctx, dbURL, appFS, migrate.Options{Logger: log.Logger}); err != nil {
		return fmt.Errorf("run app migrations: %w", err)
	}
	if err := migrate.Up(ctx, dbURL, wfmigrations.FS(), migrate.Options{
		Logger:    log.Logger,
		TableName: wfmigrations.DefaultTableName,
	}); err != nil {
		return fmt.Errorf("run workflow migrations: %w", err)
	}

	reg, err := NewFieldRegistry(DefaultFieldTypes()...)
	if err != nil {
		return err
	}
	repo := wfpg.NewRepository()
	engine := workflow.New(repo, workflow.Config{}, log.Logger)
	catalog := NewCatalog(reg, engine)
	if err := catalog.Load(ctx, pool); err != nil {
		return err
	}
	if _, ok := catalog.Latest("consulta"); !ok {
		spec, err := ParseProcessSpec(consultaJSON)
		if err != nil {
			return err
		}
		if _, err := catalog.Publish(ctx, pool, spec); err != nil {
			return err
		}
	}

	// The fake directory: "luis" is the titular encargado of "atencion".
	safo := NewFakeSAFO()
	safo.SetTitular("encargado", "atencion", "luis")

	svc := NewService(pool, engine, repo, catalog, safo)
	srv := &http.Server{
		Addr:              net.JoinHostPort("", port),
		Handler:           NewRouter(svc, catalog, safo, pool, log.Logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
