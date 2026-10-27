package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	// vogel:component worker begin
	"github.com/jackc/pgx/v5/pgxpool"

	// vogel:component worker end
	"github.com/prometheus/client_golang/prometheus"

	// vogel:component storage begin
	"github.com/kafeiih/vogel/storage/s3"

	// vogel:component storage end
	// vogel:component bpms begin
	documentmigrations "github.com/kafeiih/vogel/document/migrations"
	"github.com/kafeiih/vogel/worker"
	workflowmigrations "github.com/kafeiih/vogel/workflow/migrations"

	// vogel:component bpms end
	// vogel:component tracing begin
	"github.com/kafeiih/vogel/otel"
	"github.com/kafeiih/vogel/otel/otelpgx"

	// vogel:component tracing end
	auditpkg "github.com/kafeiih/vogel/audit"
	auditmigrations "github.com/kafeiih/vogel/audit/migrations"
	auditpg "github.com/kafeiih/vogel/audit/postgres"
	"github.com/kafeiih/vogel/auth/zitadel"
	"github.com/kafeiih/vogel/authz/cerbos"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/logger"
	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/postgres"

	// vogel:component worker begin
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/riverschema"

	// vogel:component worker end
	// vogel:component bpms begin
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms"
	bpmsmigrations "github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/bpms/migrations"

	// vogel:component bpms end
	// vogel:component tracing begin
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/tracing"

	// vogel:component tracing end
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/config"
	appmigrate "github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/database/migrate"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/database/migrations"
	httpiface "github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/handler"
)

func main() {
	args := os.Args[1:]
	var subcmd string
	if len(args) > 0 {
		subcmd = args[0]
	}
	cmd, err := parseCommand(subcmd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch cmd {
	case cmdServer:
		err = runServer(false)
	case cmdStartFromMigrate:
		err = runServer(true)
	case cmdMigrate:
		err = runMigrate(args[1:])
	}
	if err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

type command int

const (
	cmdServer command = iota
	// vogel:component worker begin
	// vogel:component worker end
	cmdStartFromMigrate
	cmdMigrate
)

func parseCommand(subcmd string) (command, error) {
	switch subcmd {
	case "", "server":
		return cmdServer, nil
	case "start-from-migrate":
		return cmdStartFromMigrate, nil
	case "migrate":
		return cmdMigrate, nil
	default:
		return 0, fmt.Errorf("unknown subcommand %q; valid: server, start-from-migrate, migrate", subcmd)
	}
}

func runMigrate(args []string) error {
	var subcmd string
	if len(args) > 0 {
		subcmd = args[0]
	}

	if subcmd == "create" {
		name := strings.TrimSpace(strings.Join(args[1:], " "))
		path, err := appmigrate.Create(appmigrate.MigrationsDir, name)
		if err != nil {
			return err
		}
		fmt.Printf("created %s\n", path)
		return nil
	}

	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		return fmt.Errorf("DB_URL environment variable is required for migrate subcommand")
	}
	requireTLS, err := config.MigrateRequiresTLS()
	if err != nil {
		return err
	}
	if requireTLS {
		if err := postgres.CheckTLS(dbURL); err != nil {
			return fmt.Errorf("DB_REQUIRE_TLS is true (the default): %w", err)
		}
	}

	ctx := context.Background()

	switch subcmd {
	case "up":
		for _, set := range migrationSets() {
			if err := migrate.Up(ctx, dbURL, set.fsys, set.opts); err != nil {
				return fmt.Errorf("running %s migrations: %w", set.name, err)
			}
		}
		// vogel:component worker begin
		if err := migrateRiver(ctx, dbURL); err != nil {
			return err
		}
		// vogel:component worker end
		return nil
	case "down":
		// vogel:component worker begin
		// vogel:component worker end
		return migrate.Down(ctx, dbURL, migrations.FS)
	case "status":
		// vogel:component worker begin
		// vogel:component worker end
		for i, set := range migrationSets() {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("%s migrations:\n", set.name)
			if err := migrate.Status(ctx, dbURL, set.fsys, os.Stdout, set.opts); err != nil {
				return fmt.Errorf("reading %s migration status: %w", set.name, err)
			}
		}
		return nil
	default:
		if subcmd == "" {
			return fmt.Errorf("migrate subcommand required; valid: up, down, status, create")
		}
		return fmt.Errorf("unknown migrate subcommand %q; valid: up, down, status, create", subcmd)
	}
}

// vogel:component worker begin
func migrateRiver(ctx context.Context, dbURL string) error {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connecting for river migrations: %w", err)
	}
	defer pool.Close()

	return riverschema.Migrate(ctx, pool, config.WorkerSchema(), slog.Default())
}

// vogel:component worker end
type migrationSet struct {
	name string
	fsys fs.FS
	opts migrate.Options
}

func migrationSets() []migrationSet {
	return []migrationSet{
		{name: "app", fsys: migrations.FS},
		{name: "audit", fsys: auditmigrations.FS(), opts: migrate.Options{TableName: auditmigrations.DefaultTableName}},
		// vogel:component bpms begin
		{name: "workflow", fsys: workflowmigrations.FS(), opts: migrate.Options{TableName: workflowmigrations.DefaultTableName}},
		{name: "document", fsys: documentmigrations.FS(), opts: migrate.Options{TableName: documentmigrations.DefaultTableName}},
		{name: "bpms", fsys: bpmsmigrations.FS, opts: migrate.Options{TableName: bpmsmigrations.TableName}},
		// vogel:component bpms end
	}
}

// vogel:component worker begin
// vogel:component worker end
func runServer(migrateFirst bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logOpts := make([]logger.Option, 0, 1)
	// vogel:component tracing begin
	logOpts = append(logOpts, logger.WithTraceIDs(otel.TraceIDs))
	// vogel:component tracing end
	appLogger := logger.NewDefault(cfg.App.Env, logOpts...).With("component", "api")
	slog.SetDefault(appLogger.Logger)
	appLogger.Info("starting application",
		"version", cfg.App.Version,
		"env", cfg.App.Env,
	)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	// vogel:component tracing begin
	// Deferred first so it runs last and flushes the spans of the final requests.
	tp, err := tracing.Setup(ctx, cfg.Tracing, "api", appLogger.Logger)
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

	if migrateFirst {
		for _, set := range migrationSets() {
			set.opts.Logger = appLogger.Logger
			if err := migrate.Up(ctx, cfg.Database.URL, set.fsys, set.opts); err != nil {
				return fmt.Errorf("running %s migrations: %w", set.name, err)
			}
		}
		// vogel:component worker begin
		if err := riverschema.Migrate(ctx, pool, cfg.Worker.Schema, appLogger.Logger); err != nil {
			return err
		}
		// vogel:component worker end
	}

	if _, err := postgres.NewPoolMetricsCollector(pool, prometheus.DefaultRegisterer); err != nil {
		return fmt.Errorf("registering pool metrics: %w", err)
	}
	metricsOpts := make([]vmw.MetricsOption, 0, 1)
	// vogel:component tracing begin
	metricsOpts = append(metricsOpts, vmw.WithExemplars(otel.SampledTraceIDs))
	// vogel:component tracing end
	metrics, err := vmw.NewMetrics(prometheus.DefaultRegisterer, metricsOpts...)
	if err != nil {
		return fmt.Errorf("building http metrics: %w", err)
	}

	metricsSrv := httpiface.NewMetricsServer(cfg.Metrics.ListenAddr, appLogger.Logger)
	metricErrCh, err := httpiface.StartMetricsServer(metricsSrv, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("starting metrics server: %w", err)
	}
	go httpiface.WatchMetricsErrors(metricErrCh, appLogger.Logger)

	zitadelCtx, zitadelCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer zitadelCancel()

	// vogel:component tracing begin
	cfg.Zitadel.WrapTransport = otel.Transport()
	// vogel:component tracing end
	authenticator, err := zitadel.New(zitadelCtx, cfg.Zitadel)
	if err != nil {
		return fmt.Errorf("initializing zitadel: %w", err)
	}

	appLogger.Info("zitadel authenticator initialized",
		"introspection", cfg.Zitadel.Introspection != nil,
		"max_token_lifetime", cfg.Zitadel.MaxTokenLifetime,
		"azp_restricted", len(cfg.Zitadel.AllowedAuthorizedParties) > 0)

	cerbosChecker, err := cerbos.New(cfg.Cerbos)
	if err != nil {
		return fmt.Errorf("initializing cerbos: %w", err)
	}
	defer func() { _ = cerbosChecker.Close() }()

	if err := probeCerbos(context.Background(), cerbosChecker, cfg.Cerbos); err != nil {
		return err
	}

	appLogger.Info("cerbos checker initialized", "host", cfg.Cerbos.Host)

	auditRepo := auditpg.NewRepository(pool)

	auditRecorder := auditpkg.NewRecorder(auditRepo)
	_ = auditRecorder // Remove once command handlers take it, with audit.SourceHTTP.

	// vogel:component bpms begin
	var processHandler *handler.ProcessHandler
	// vogel:component bpms end
	// vogel:component storage begin
	if len(cfg.Storages) > 0 {
		// vogel:component tracing begin
		for name, sc := range cfg.Storages {
			sc.WrapTransport = otel.Transport()
			cfg.Storages[name] = sc
		}
		// vogel:component tracing end
		storageReg, err := s3.NewStorageRegistry(cfg.Storages, appLogger.Logger)
		if err != nil {
			return fmt.Errorf("initializing storage: %w", err)
		}
		_ = storageReg // Remove once handlers use storage.
		appLogger.Info("storage initialized", "backends", storageReg.Names())
		// vogel:component bpms begin

		bpmsQueue, err := worker.NewRiverQueue(pool, nil, cfg.Worker, appLogger.Logger)
		if err != nil {
			return fmt.Errorf("creating bpms queue: %w", err)
		}
		bpmsDeps := bpms.Deps{
			DB:        pool,
			Queue:     bpmsQueue,
			Storage:   storageReg.Get(cfg.BPMS.DocumentStorage),
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
		processesCtx, processesCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer processesCancel()
		processes, err := bpms.NewDomain(processesCtx, pool, bpmsBase.Engine, bpms.NewMemoryDirectory(profileHolders(cfg.BPMS.ProfileHolders)...))
		if err != nil {
			return fmt.Errorf("initializing bpms processes: %w", err)
		}
		processHandler = handler.NewProcessHandler(processes.Service, appLogger.Logger)
		appLogger.Info("bpms initialized", "definitions", len(bpmsBase.Engine.Definitions()), "profile_holders", len(cfg.BPMS.ProfileHolders))
		// vogel:component bpms end
	}

	// vogel:component storage end
	healthHandler := handler.NewHealthHandler(pool, appLogger.Logger)
	auditHandler := handler.NewAuditHandler(auditRepo, appLogger.Logger)

	routerConfig := httpiface.RouterConfig{
		AppURL:      cfg.App.Url,
		Timeout:     cfg.App.Timeout,
		CORSOrigins: cfg.CORS.AllowedOrigins,

		TrustedProxies: cfg.App.TrustedProxies,

		EnableSwagger: cfg.App.SwaggerEnabled,
	}

	deps := httpiface.Dependencies{
		Config:        routerConfig,
		Authenticator: authenticator,
		AuthzChecker:  cerbosChecker,
		Metrics:       metrics,
		Logger:        appLogger.Logger,
		HealthHandler: healthHandler,
		AuditHandler:  auditHandler,
	}
	// vogel:component bpms begin
	deps.ProcessHandler = processHandler
	// vogel:component bpms end
	// vogel:component tracing begin
	deps.Tracing = otel.Middleware()
	// vogel:component tracing end

	router := httpiface.NewRouter(deps)

	server := httpiface.NewServer(cfg.App.Port, router, appLogger.Logger)

	appLogger.Info("server ready to accept connections", "port", cfg.App.Port)

	return server.Start(httpiface.MetricsDrainer(metricsSrv, appLogger.Logger))
}

// vogel:component bpms begin
func profileHolders(in []config.ProfileHolder) []bpms.ProfileHolder {
	out := make([]bpms.ProfileHolder, 0, len(in))
	for _, h := range in {
		out = append(out, bpms.ProfileHolder{Position: h.Position, Unit: h.Unit, UserID: h.UserID})
	}
	return out
}

// vogel:component bpms end
var (
	cerbosProbeTimeout    = 5 * time.Second
	cerbosProbeBudget     = 30 * time.Second
	cerbosProbeBackoff    = 500 * time.Millisecond
	cerbosProbeMaxBackoff = 4 * time.Second
)

type pinger interface {
	Ping(ctx context.Context) error
}

func probeCerbos(ctx context.Context, p pinger, cfg cerbos.Config) error {
	deadline := time.Now().Add(cerbosProbeBudget)
	backoff := cerbosProbeBackoff
	for {
		err := pingOnce(ctx, p)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || time.Now().Add(backoff).After(deadline) {
			mode := "TLS"
			if cfg.Plaintext {
				mode = "plaintext"
			}
			return fmt.Errorf("cerbos PDP at %s is unreachable over %s after retrying for %s (TLS is the default; set CERBOS_PLAINTEXT=true only for a PDP without TLS): %w", cfg.Host, mode, cerbosProbeBudget, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, cerbosProbeMaxBackoff)
	}
}

func pingOnce(ctx context.Context, p pinger) error {
	ctx, cancel := context.WithTimeout(ctx, cerbosProbeTimeout)
	defer cancel()
	return p.Ping(ctx)
}
