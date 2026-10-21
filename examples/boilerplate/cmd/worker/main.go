package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"

	// vogel:component notification begin
	"github.com/kafeiih/vogel/notification"
	"github.com/kafeiih/vogel/notification/sendgrid"
	"github.com/kafeiih/vogel/notification/smtp"

	// vogel:component notification end
	// vogel:component bpms begin
	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/storage/s3"

	// vogel:component bpms end
	// vogel:component tracing begin
	"github.com/kafeiih/vogel/otel"
	"github.com/kafeiih/vogel/otel/otelpgx"
	"github.com/kafeiih/vogel/otel/otelriver"

	// vogel:component tracing end
	"github.com/kafeiih/vogel/logger"
	"github.com/kafeiih/vogel/postgres"
	"github.com/kafeiih/vogel/worker"

	// vogel:component bpms begin
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms"

	// vogel:component bpms end
	// vogel:component tracing begin
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/tracing"

	// vogel:component tracing end
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
		if err := checkHeartbeat(heartbeatPath(), time.Now(), heartbeatMaxAge); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(cmd == cmdStartFromMigrate); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

type command int

const (
	cmdServer command = iota
	cmdStartFromMigrate
	cmdHealthcheck
)

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

func run(migrateFirst bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logOpts := make([]logger.Option, 0, 1)
	// vogel:component tracing begin
	logOpts = append(logOpts, logger.WithTraceIDs(otel.TraceIDs))
	// vogel:component tracing end
	appLogger := logger.NewDefault(cfg.App.Env, logOpts...).With("component", "worker")
	slog.SetDefault(appLogger.Logger)
	appLogger.Info("starting worker",
		"version", cfg.App.Version,
		"env", cfg.App.Env,
	)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	// vogel:component tracing begin
	// Deferred first so it runs last and flushes the spans of the final requests.
	tp, err := tracing.Setup(ctx, cfg.Tracing, "worker", appLogger.Logger)
	if err != nil {
		return err
	}
	defer tracing.Shutdown(tp, appLogger.Logger)
	cfg.Database.Tracer = otelpgx.NewTracer()
	cfg.Database.TraceIDs = otel.SampledTraceIDs
	// vogel:component tracing end
	pool, err := postgres.NewPool(ctx, cfg.Database, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	appLogger.Info("database connection established")

	// Jobs that record audit entries must use audit.SourceWorker, never audit.SourceHTTP.

	// vogel:component notification begin
	var notifier notification.Notifier
	switch cfg.Notification.Provider {
	case "sendgrid":
		notifier, err = sendgrid.NewSendGridNotifier(
			cfg.Notification.SendGrid, cfg.Notification.DefaultFrom, appLogger.Logger,
		)
	default:
		notifier, err = smtp.NewSMTPNotifier(
			cfg.Notification.SMTP, cfg.Notification.DefaultFrom, appLogger.Logger,
		)
	}
	if err != nil {
		appLogger.Warn("notification disabled — notifier not configured", "error", err)
		notifier = nil
	}
	// vogel:component tracing begin
	if notifier != nil {
		notifier = otel.Notifier(notifier)
	}
	// vogel:component tracing end

	// vogel:component notification end
	workers := river.NewWorkers()
	river.AddWorker(workers, &jobs.ExampleWorker{})
	// vogel:component notification begin
	if notifier != nil {
		river.AddWorker(workers, &jobs.SendEmailWorker{Notifier: notifier})
		appLogger.Info("send_email worker registered", "provider", cfg.Notification.Provider)
	}
	// vogel:component notification end
	// vogel:component bpms begin

	documentStorageCfg := cfg.Storages[cfg.BPMS.DocumentStorage]
	// vogel:component tracing begin
	documentStorageCfg.WrapTransport = otel.Transport()
	// vogel:component tracing end
	documentStorage, err := s3.NewS3Storage(documentStorageCfg, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("initializing document storage: %w", err)
	}
	bpmsQueue, err := worker.NewRiverQueue(pool, nil, cfg.Worker, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("creating bpms queue: %w", err)
	}
	bpmsDeps := bpms.Deps{
		DB:        pool,
		Queue:     bpmsQueue,
		Storage:   documentStorage,
		Gotenberg: cfg.BPMS.Gotenberg,
		Logger:    appLogger.Logger,
	}
	// vogel:component tracing begin
	bpmsDeps.WrapTransport = otel.Transport()
	// vogel:component tracing end
	bpmsBase, err := bpms.New(bpmsDeps)
	if err != nil {
		return fmt.Errorf("initializing bpms: %w", err)
	}
	if err := action.AddWorker(workers, action.NewWorker(bpmsBase.Registry)); err != nil {
		return fmt.Errorf("registering bpms action worker: %w", err)
	}
	appLogger.Info("bpms action worker registered")
	// vogel:component bpms end

	if _, err := postgres.NewPoolMetricsCollector(pool, prometheus.DefaultRegisterer); err != nil {
		return fmt.Errorf("registering pool metrics: %w", err)
	}
	metricsSrv, stopMetrics, err := startMetricsServer(cfg.Metrics.WorkerListenAddr, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("starting metrics server: %w", err)
	}
	defer stopMetrics()
	appLogger.Info("worker metrics server listening", "addr", metricsSrv.Addr)

	queue, err := worker.NewRiverQueue(pool, workers, cfg.Worker, appLogger.Logger,
		worker.WithMetrics(prometheus.DefaultRegisterer),
		// vogel:component tracing begin
		worker.WithMiddleware(otelriver.NewMiddleware()),
		// vogel:component tracing end
	)
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

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	if err := queue.Start(workerCtx); err != nil {
		return fmt.Errorf("starting worker: %w", err)
	}

	appLogger.Info("worker ready to process jobs",
		"schema", cfg.Worker.Schema,
	)

	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		runHeartbeat(hbCtx, heartbeatPath(), heartbeatInterval, appLogger.Logger)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	<-sigCh
	appLogger.Info("shutdown signal received, stopping worker...")
	hbCancel()
	<-hbDone

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
