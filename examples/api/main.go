// Command api is a runnable example that boots a real HTTP API wiring EVERY
// vogel package together, so a reader learns the composition root by
// reading this one directory.
//
// Postgres is real (see docker-compose.yaml). auth, authz, storage, and
// notification get tiny in-memory fakes -- see fakes.go -- implementing
// vogel's ports; that is the point of this example: swapping a fake for
// auth/zitadel, authz/cerbos, storage/s3, or notification/smtp is a
// one-line change in run(), step 7 below, because every consumer depends
// only on the port interface.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kafeiih/vogel/audit"
	audithttpx "github.com/kafeiih/vogel/audit/httpx"
	auditmigrations "github.com/kafeiih/vogel/audit/migrations"
	auditpg "github.com/kafeiih/vogel/audit/postgres"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/logger"
	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/postgres"
	"github.com/kafeiih/vogel/worker"
	"github.com/kafeiih/vogel/workflow"
	wfmigrations "github.com/kafeiih/vogel/workflow/migrations"
	wfpg "github.com/kafeiih/vogel/workflow/postgres"
)

//go:embed migrations/*.sql
var appMigrations embed.FS

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		// The real work lives in run(), not here, precisely so that every
		// defer it registers (pool.Close, stop, cancel, ...) still runs on
		// both the success and the graceful-shutdown paths: os.Exit skips
		// every pending defer in the calling goroutine, so it is confined to
		// this single line, reached only after run() has already returned.
		os.Exit(1)
	}
}

func run() error {
	// 0. Populate the environment from .env if one is present, so that
	// `go run .` works straight after `cp env.example .env` with no shell
	// ceremony. A missing file is a no-op and real environment variables are
	// never overwritten -- see loadDotEnv.
	if err := loadDotEnv(".env"); err != nil {
		return fmt.Errorf("load .env: %w", err)
	}

	// 1. Load and validate configuration up front, so every misconfiguration
	// is reported in a single boot failure -- see LoadConfig's doc comment.
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 2. Build the logger before anything else that can fail, so every later
	// step can log through it.
	log := logger.New(cfg.Env, os.Stdout)

	// 3. A context canceled on SIGINT/SIGTERM drives the graceful shutdown
	// sequence at the bottom of this function.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 4. The connection pool every other component below is built from.
	pool, err := postgres.NewPool(ctx, postgres.Config{
		URL:        cfg.DatabaseURL,
		MaxConns:   10,
		MinConns:   2,
		RequireTLS: false,
	}, log.Logger)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	// 5. Three independent migration sets, each against its OWN goose
	// version table:
	//   - this example's own migrations (documents) use goose's default
	//     table ("goose_db_version"), since this binary owns no other
	//     migration set that could collide with it;
	//   - audit/migrations owns audit_log, numbered independently starting
	//     at 001 of its own -- without a distinct TableName its "001" would
	//     be indistinguishable from this example's own "001";
	//   - workflow/migrations owns workflow_case/workflow_event, numbered
	//     independently for the same reason.
	// Running all three against one shared table would make goose believe
	// one library's "001" was the same migration as another's, silently
	// skipping one set entirely.
	if err := migrate.Up(ctx, cfg.DatabaseURL, appMigrations, migrate.Options{Logger: log.Logger}); err != nil {
		return fmt.Errorf("run app migrations: %w", err)
	}
	if err := migrate.Up(ctx, cfg.DatabaseURL, auditmigrations.FS(), migrate.Options{
		Logger:    log.Logger,
		TableName: auditmigrations.DefaultTableName,
	}); err != nil {
		return fmt.Errorf("run audit migrations: %w", err)
	}
	if err := migrate.Up(ctx, cfg.DatabaseURL, wfmigrations.FS(), migrate.Options{
		Logger:    log.Logger,
		TableName: wfmigrations.DefaultTableName,
	}); err != nil {
		return fmt.Errorf("run workflow migrations: %w", err)
	}

	// 6. Observability: a private registry (not the global default) so this
	// example never collides with another vogel-based process's metric
	// names when both run inside the same test binary.
	reg := prometheus.NewRegistry()
	if _, err := postgres.NewPoolMetricsCollector(pool, reg); err != nil {
		return fmt.Errorf("register pool metrics: %w", err)
	}
	metrics, err := vmw.NewMetrics(reg)
	if err != nil {
		return fmt.Errorf("build http metrics: %w", err)
	}

	// 7. This is the composition root's central move: every port below is
	// wired to an in-memory fake FOR THIS EXAMPLE ONLY (see fakes.go).
	// Swapping any one of them for its real adapter --
	// auth/zitadel.Authenticator, authz/cerbos.Checker,
	// storage/s3.S3Storage, notification/smtp.SMTPNotifier or
	// notification/sendgrid.SendGridNotifier -- is a one-line change right
	// here: every consumer downstream (the middleware stack, the handlers,
	// the worker) depends only on the port interface, never on the fake's
	// or the real adapter's concrete type.
	authenticator := fakeAuthenticator{}
	checker := fakeChecker{}
	fileStorage := newFakeStorage()
	notifier := &fakeNotifier{logger: log.Logger}

	documentStore := NewDocumentStore()

	auditRepo := auditpg.NewRepository(pool)
	recorder := audit.NewRecorder(auditRepo)
	auditHandler, err := audithttpx.NewHandler(auditRepo, log.Logger)
	if err != nil {
		return fmt.Errorf("build audit handler: %w", err)
	}

	wfRepo := wfpg.NewRepository()
	engine := workflow.New(wfRepo, workflow.Config{}, log.Logger, workflow.WithDefinitions(ApprovalDefinition()))
	if err := engine.RegisterGuard("assigned", AssignedGuard); err != nil {
		return fmt.Errorf("register workflow guard: %w", err)
	}

	txManager := pgxtx.NewPgxTxManager(pool)

	// 8. worker.EnsureSchema MUST run before RiverQueue.Migrate on a fresh
	// database: River creates its tables INSIDE the configured schema but
	// never creates the schema itself. Config.Schema has no library default
	// on purpose -- every consumer must pick one explicitly, since two
	// vogel-based services sharing a database would otherwise silently
	// collide on whatever default this library chose.
	//
	// This example runs the API and the worker in ONE process for
	// simplicity: RegisterWorkers below returns a non-nil *river.Workers, so
	// NewRiverQueue builds a client that both enqueues AND processes jobs.
	// In production you would normally split them: the API binary builds
	// its queue with workers == nil (insert-only -- it only ever calls
	// Enqueue/EnqueueTx) and a separate worker binary builds its own queue
	// with the real *river.Workers and calls Start.
	if err := worker.EnsureSchema(ctx, pool, cfg.RiverSchema); err != nil {
		return fmt.Errorf("ensure river schema: %w", err)
	}

	notifyWorker := &NotifyReviewersWorker{
		notifier: notifier,
		recorder: recorder,
		pool:     pool,
		from:     cfg.NotifyFrom,
		logger:   log.Logger,
	}

	queue, err := worker.NewRiverQueue(pool, RegisterWorkers(notifyWorker), worker.Config{
		Schema:            cfg.RiverSchema,
		DefaultMaxWorkers: 10,
	}, log.Logger)
	if err != nil {
		return fmt.Errorf("build river queue: %w", err)
	}
	if err := queue.Migrate(ctx); err != nil {
		return fmt.Errorf("run river migrations: %w", err)
	}
	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start river queue: %w", err)
	}

	docs := NewDocumentHandler(documentStore, engine, recorder, queue, fileStorage, txManager, pool, log.Logger)

	// 9. Every timeout below is set explicitly (gosec G112): a slow or
	// hanging client must never be able to hold a connection, and the
	// goroutine serving it, open indefinitely.
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           NewRouter(docs, auditHandler, authenticator, checker, metrics, pool, reg, log.Logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 10. Serve in a goroutine so this function can select on ctx.Done()
	// below. http.ErrServerClosed is the expected error produced by our own
	// Shutdown call below, so it is filtered out rather than reported.
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil

	case <-ctx.Done():
		log.Info("shutting down")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()

		// Shutdown, then Stop, then Close (deferred at step 4 above), in
		// that exact order: first stop accepting new requests so no new work
		// starts, then let River finish (or time out) whatever jobs were
		// already in flight before severing its own database connections,
		// and only then release the pool itself -- after both the HTTP
		// server and River are done using it.
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("http server shutdown", "error", err)
		}
		if err := queue.Stop(shutdownCtx); err != nil {
			log.Error("river queue shutdown", "error", err)
		}
		return nil
	}
}
