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
	"github.com/kafeiih/vogel/logger"
	// vogel:component tracing begin
	"github.com/kafeiih/vogel/otel"
	"github.com/kafeiih/vogel/otel/otelpgx"
	"github.com/kafeiih/vogel/otel/otelriver"

	// vogel:component tracing end
	"github.com/kafeiih/vogel/postgres"
	"github.com/kafeiih/vogel/worker"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/application/jobs"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/config"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/riverschema"
	// vogel:component tracing begin
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/tracing"
	// vogel:component tracing end
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
		// slog, not log.Fatal: once run installs the app logger as the default,
		// the log package writes at INFO, and a fatal error must stay an ERROR.
		slog.Error("exiting", "error", err)
		os.Exit(1)
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

	var logOpts []logger.Option
	// vogel:component tracing begin
	// Log lines written with a context carry the span's trace_id/span_id.
	logOpts = append(logOpts, logger.WithTraceIDs(otel.TraceIDs))
	// vogel:component tracing end
	appLogger := logger.NewDefault(cfg.App.Env, logOpts...).With("component", "worker")
	// Installed as the default too, so package-level slog calls, River's
	// migrations and libraries logging through slog.Default() share its JSON
	// format, component attribute and request_id from context. It also routes
	// the standard log package through it, as INFO records.
	slog.SetDefault(appLogger.Logger)
	appLogger.Info("starting worker",
		"version", cfg.App.Version,
		"env", cfg.App.Env,
	)

	// Database connection
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	// vogel:component tracing begin
	// Deferred before everything else, so it runs last: the spans of the
	// jobs the queue finishes while stopping are flushed after it stops.
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

	// Every mutation this worker records via an audit Recorder must use
	// audit.SourceWorker, never audit.SourceHTTP -- there is no inbound HTTP
	// request here.

	// vogel:component notification begin
	// Build notifier based on config (smtp or sendgrid).
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
	// vogel:component tracing begin
	if notifier != nil {
		// A notification.send span per email, with no address or subject.
		notifier = otel.Notifier(notifier)
	}
	// vogel:component tracing end

	// vogel:component notification end
	// Register job workers
	workers := river.NewWorkers()
	river.AddWorker(workers, &jobs.ExampleWorker{})
	// vogel:component notification begin
	if notifier != nil {
		river.AddWorker(workers, &jobs.SendEmailWorker{Notifier: notifier})
		appLogger.Info("send_email worker registered", "provider", cfg.Notification.Provider)
	}
	// vogel:component notification end
	// Register additional workers here:
	// river.AddWorker(workers, &jobs.GeneratePDFWorker{})

	// Job and pool metrics, served on the worker's own /metrics listener
	// (WORKER_METRICS_LISTEN_ADDR, default :9091) from the default registry.
	if _, err := postgres.NewPoolMetricsCollector(pool, prometheus.DefaultRegisterer); err != nil {
		return fmt.Errorf("registering pool metrics: %w", err)
	}
	metricsSrv, stopMetrics, err := startMetricsServer(cfg.Metrics.WorkerListenAddr, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("starting metrics server: %w", err)
	}
	defer stopMetrics()
	appLogger.Info("worker metrics server listening", "addr", metricsSrv.Addr)

	// Create River queue. worker.WithMetrics records
	// river_job_duration_seconds{kind,outcome}. No periodic jobs are
	// registered by this boilerplate -- if you add one, pass
	// worker.WithPeriodicJobs(...) here.
	queueOpts := []worker.Option{worker.WithMetrics(prometheus.DefaultRegisterer)}
	// vogel:component tracing begin
	// A span per job attempt, continuing the trace of the request that
	// enqueued it (carried in the job metadata).
	queueOpts = append(queueOpts, worker.WithMiddleware(otelriver.NewMiddleware()))
	// vogel:component tracing end
	queue, err := worker.NewRiverQueue(pool, workers, cfg.Worker, appLogger.Logger, queueOpts...)
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
