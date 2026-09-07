package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/logger"
	"github.com/kafeiih/vogel/notification"
	"github.com/kafeiih/vogel/notification/sendgrid"
	"github.com/kafeiih/vogel/notification/smtp"
	"github.com/kafeiih/vogel/postgres"
	"github.com/kafeiih/vogel/worker"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/jobs"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/config"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	appLogger := logger.NewDefault(cfg.App.Env)
	appLogger.Info("starting worker",
		"version", cfg.App.Version,
		"env", cfg.App.Env,
	)

	// Database connection
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg.Database, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	appLogger.Info("database connection established")

	// Build notifier based on config (smtp or sendgrid). Every mutation this
	// worker records via the audit Recorder below must use audit.SourceWorker,
	// never audit.SourceHTTP -- there is no inbound HTTP request here.
	var notifier notification.Notifier
	switch cfg.Notification.Provider {
	case "sendgrid":
		notifier, err = sendgrid.NewSendGridNotifier(
			cfg.Notification.SendGrid, cfg.Notification.DefaultFrom, appLogger.Logger,
		)
	default: // "smtp"
		notifier, err = smtp.NewSMTPNotifier(
			cfg.Notification.SMTP, cfg.Notification.DefaultFrom, appLogger.Logger,
		)
	}
	if err != nil {
		appLogger.Warn("notification disabled — notifier not configured", "error", err)
		notifier = nil
	}

	// Register job workers
	workers := river.NewWorkers()
	river.AddWorker(workers, &jobs.ExampleWorker{})
	if notifier != nil {
		river.AddWorker(workers, &jobs.SendEmailWorker{Notifier: notifier})
		appLogger.Info("send_email worker registered", "provider", cfg.Notification.Provider)
	}
	// Register additional workers here:
	// river.AddWorker(workers, &jobs.GeneratePDFWorker{})

	// Create River queue. No periodic jobs are registered by this
	// boilerplate -- if you add one, pass worker.WithPeriodicJobs(...) here.
	queue, err := worker.NewRiverQueue(pool, workers, cfg.Worker, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("creating river queue: %w", err)
	}

	// Run River schema migrations
	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer migrateCancel()

	// River creates its tables INSIDE cfg.Worker.Schema but does not create
	// the schema itself. On a freshly provisioned DB this would fail with
	// SQLSTATE 3F000. EnsureSchema is idempotent.
	if err := worker.EnsureSchema(migrateCtx, pool, cfg.Worker.Schema); err != nil {
		return fmt.Errorf("ensuring worker schema: %w", err)
	}

	if err := queue.Migrate(migrateCtx); err != nil {
		return fmt.Errorf("running river migrations: %w", err)
	}

	appLogger.Info("river migrations applied")

	// Start processing jobs
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	if err := queue.Start(workerCtx); err != nil {
		return fmt.Errorf("starting worker: %w", err)
	}

	appLogger.Info("worker ready to process jobs",
		"schema", cfg.Worker.Schema,
	)

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	<-sigCh
	appLogger.Info("shutdown signal received, stopping worker...")

	// Soft stop: wait for in-flight jobs to complete
	softCtx, softCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer softCancel()

	if err := queue.Stop(softCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			appLogger.Warn("soft stop timeout, cancelling worker context")
			workerCancel()
		} else {
			return fmt.Errorf("stopping worker: %w", err)
		}
	}

	appLogger.Info("worker stopped gracefully")
	return nil
}
