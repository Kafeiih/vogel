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
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/riverschema"
)

func main() {
	var subcmd string
	if len(os.Args) > 1 {
		subcmd = os.Args[1]
	}
	cmd, err := parseCommand(subcmd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if cmd == cmdHealthcheck {
		// Reads the heartbeat only: no config, no database, so it is cheap
		// enough to run every few seconds from the container's HEALTHCHECK.
		if err := checkHeartbeat(heartbeatPath(), time.Now(), heartbeatMaxAge); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(cmd == cmdStartFromMigrate); err != nil {
		log.Fatal(err)
	}
}

// command is a top-level subcommand of the worker binary. It mirrors the api
// binary's phases: River's schema is applied by "api migrate up", so the
// worker's role does not need CREATE privileges and replicas do not race to
// migrate at boot.
type command int

const (
	// cmdServer processes jobs only and never touches the schema. A bare
	// "worker" is an alias for "worker server".
	cmdServer command = iota
	// cmdStartFromMigrate ensures River's schema and applies its migrations,
	// then processes jobs. It is a development convenience: the worker's role
	// must be able to create the schema and its tables.
	cmdStartFromMigrate
	// cmdHealthcheck exits 0 when a running "worker server" in the same
	// container has written a fresh heartbeat, 1 otherwise. It is the
	// image's HEALTHCHECK; see heartbeat.go.
	cmdHealthcheck
)

// parseCommand maps the first CLI argument to a command.
func parseCommand(subcmd string) (command, error) {
	switch subcmd {
	case "", "server":
		return cmdServer, nil
	case "start-from-migrate":
		return cmdStartFromMigrate, nil
	case "healthcheck":
		return cmdHealthcheck, nil
	default:
		return 0, fmt.Errorf("unknown subcommand %q; valid: server, start-from-migrate, healthcheck", subcmd)
	}
}

// run processes jobs until SIGINT/SIGTERM. With migrateFirst
// ("start-from-migrate") it first applies River's schema against
// cfg.Database.URL; without it ("server") the schema must already be current,
// applied by "api migrate up" with a migrator role.
func run(migrateFirst bool) error {
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

	if migrateFirst {
		migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer migrateCancel()

		if err := riverschema.Migrate(migrateCtx, pool, cfg.Worker.Schema, appLogger.Logger); err != nil {
			return err
		}
		appLogger.Info("river migrations applied")
	}

	// Start processing jobs
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	if err := queue.Start(workerCtx); err != nil {
		return fmt.Errorf("starting worker: %w", err)
	}

	appLogger.Info("worker ready to process jobs",
		"schema", cfg.Worker.Schema,
	)

	// Liveness for the container healthcheck ("worker healthcheck").
	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		runHeartbeat(hbCtx, heartbeatPath(), heartbeatInterval, appLogger.Logger)
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	<-sigCh
	appLogger.Info("shutdown signal received, stopping worker...")
	hbCancel()
	<-hbDone

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
