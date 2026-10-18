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
	"github.com/kafeiih/vogel/examples/boilerplate/docs"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/config"
	appmigrate "github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/database/migrate"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/database/migrations"
	httpiface "github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/handler"
)

//	@title			Go Bluprint API
//	@version		1.0
//	@description	Boilerplate para microservicios institucionales/gubernamentales.
//	@termsOfService	http://swagger.io/terms/

//	@contact.name	API Support
//	@contact.url	http://www.swagger.io/support
//	@contact.email	support@swagger.io

//	@license.name	Apache 2.0
//	@license.url	http://www.apache.org/licenses/LICENSE-2.0.html

// @BasePath					/v1
//
// @securityDefinitions.apikey	ApiKeyAuth
// @in							header
// @name						Authorization
// @description				Type "Bearer" followed by a space and JWT token.
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
		// slog, not log.Fatal: once run installs the app logger as the default,
		// the log package writes at INFO, and a fatal error must stay an ERROR.
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

// command is a top-level subcommand of the api binary. The startup phases
// follow Zitadel's setup/start split: migrations are a separate step so the
// serving role does not need to own the schema.
type command int

const (
	// cmdServer serves only and never touches the schema. A bare "api" is
	// an alias for "api server".
	cmdServer command = iota
	// cmdStartFromMigrate applies every migration set and then serves.
	// vogel:component worker begin
	// River's schema is applied too, as "migrate up" does.
	// vogel:component worker end
	// It is a development convenience: the serving role must own the tables.
	cmdStartFromMigrate
	// cmdMigrate runs the migrate subcommands (up, down, status, create).
	cmdMigrate
)

// parseCommand maps the first CLI argument to a command.
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

	// create does not need a database connection — it only scaffolds a file.
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
		// Only this service's own set is rolled back. migrate.Down reverts the
		// latest migration of the set it is given: on the audit set that would
		// first remove audit_log's append-only triggers (002) and, on a second
		// "migrate down", drop audit_log and the audit trail with it (001).
		// cmd/api/migrate_integration_test.go proves the audit set is left
		// untouched.
		// vogel:component worker begin
		// River's schema is never rolled back either, and the same test
		// proves it: its down migrations drop the job tables and every queued
		// job with them.
		// vogel:component worker end
		return migrate.Down(ctx, dbURL, migrations.FS)
	case "status":
		// Lists the goose sets only.
		// vogel:component worker begin
		// River's migrations are not goose migrations; "migrate up" applies
		// them idempotently and logs each version it applies.
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
// migrateRiver ensures the worker's River schema (WORKER_SCHEMA, the same
// variable the worker reads) and applies River's migrations, so "worker
// server" can start with a role that cannot create tables.
func migrateRiver(ctx context.Context, dbURL string) error {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connecting for river migrations: %w", err)
	}
	defer pool.Close()

	return riverschema.Migrate(ctx, pool, config.WorkerSchema(), slog.Default())
}

// vogel:component worker end
// migrationSet is one embedded set of goose migrations and the options it
// runs with.
type migrationSet struct {
	name string
	fsys fs.FS
	opts migrate.Options
}

// migrationSets lists every migration set this service applies, in order.
// "start-from-migrate" and "migrate up" both read it, so a set added here is
// applied by either. Each set numbers from 001, so
// each needs its own goose version table.
func migrationSets() []migrationSet {
	return []migrationSet{
		{name: "app", fsys: migrations.FS},
		{name: "audit", fsys: auditmigrations.FS(), opts: migrate.Options{TableName: auditmigrations.DefaultTableName}},
	}
}

// runServer serves the API. With migrateFirst ("start-from-migrate") it first
// applies every migration set against cfg.Database.URL.
// vogel:component worker begin
// River's schema is applied the same way.
// vogel:component worker end
// Without it ("server") the schema must already be current, applied by
// "migrate up" with a migrator role.
func runServer(migrateFirst bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	appLogger := logger.NewDefault(cfg.App.Env).With("component", "api")
	// Installed as the default too, so package-level slog calls, River's
	// migrations and libraries logging through slog.Default() share its JSON
	// format, component attribute and request_id from context. It also routes
	// the standard log package through it, as INFO records.
	slog.SetDefault(appLogger.Logger)
	appLogger.Info("starting application",
		"version", cfg.App.Version,
		"env", cfg.App.Env,
	)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg.Database, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	appLogger.Info("database connection established")

	// Two independent migration sets, each against its OWN goose version
	// table: this application's own migrations (default table,
	// "goose_db_version", since this binary owns no other migration set that
	// could collide with it) and audit/migrations, numbered independently
	// starting at its own "001" (table "vogel_db_version"). Running both
	// against one shared table would make goose believe one set's "001" was
	// the other's, silently skipping one entirely. See the "Tres conjuntos
	// de migraciones" section of vogel's examples/api/README.md — this
	// boilerplate only needs two of the three since it does not use
	// vogel/workflow.
	if migrateFirst {
		for _, set := range migrationSets() {
			set.opts.Logger = appLogger.Logger
			if err := migrate.Up(ctx, cfg.Database.URL, set.fsys, set.opts); err != nil {
				return fmt.Errorf("running %s migrations: %w", set.name, err)
			}
		}
		// vogel:component worker begin
		// Same River step as "migrate up", so both leave the database alike.
		if err := riverschema.Migrate(ctx, pool, cfg.Worker.Schema, appLogger.Logger); err != nil {
			return err
		}
		// vogel:component worker end
	}

	// Database pool metrics for Prometheus, registered on the default
	// registerer — the same one promhttp.Handler() (used by
	// NewMetricsServer, in metrics_server.go) reads from.
	if _, err := postgres.NewPoolMetricsCollector(pool, prometheus.DefaultRegisterer); err != nil {
		return fmt.Errorf("registering pool metrics: %w", err)
	}
	metrics, err := vmw.NewMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return fmt.Errorf("building http metrics: %w", err)
	}

	// Dedicated metrics server on its own port (METRICS_LISTEN_ADDR, default :9090).
	// Keeps /metrics off the public API port.
	metricsSrv := httpiface.NewMetricsServer(cfg.Metrics.ListenAddr, appLogger.Logger)
	metricErrCh, err := httpiface.StartMetricsServer(metricsSrv, appLogger.Logger)
	if err != nil {
		return fmt.Errorf("starting metrics server: %w", err)
	}
	go httpiface.WatchMetricsErrors(metricErrCh, appLogger.Logger)

	// Zitadel (bounded context for JWKS fetch during init)
	zitadelCtx, zitadelCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer zitadelCancel()

	authenticator, err := zitadel.New(zitadelCtx, cfg.Zitadel)
	if err != nil {
		return fmt.Errorf("initializing zitadel: %w", err)
	}

	appLogger.Info("zitadel authenticator initialized",
		"introspection", cfg.Zitadel.Introspection != nil,
		"max_token_lifetime", cfg.Zitadel.MaxTokenLifetime,
		"azp_restricted", len(cfg.Zitadel.AllowedAuthorizedParties) > 0)

	// Cerbos authorization (PDP via gRPC)
	cerbosChecker, err := cerbos.New(cfg.Cerbos)
	if err != nil {
		return fmt.Errorf("initializing cerbos: %w", err)
	}
	defer func() { _ = cerbosChecker.Close() }()

	if err := probeCerbos(context.Background(), cerbosChecker, cfg.Cerbos); err != nil {
		return err
	}

	appLogger.Info("cerbos checker initialized", "host", cfg.Cerbos.Host)

	// Audit subsystem
	auditRepo := auditpg.NewRepository(pool)

	// auditRecorder is passed to command handlers that need to record
	// mutations, with audit.SourceHTTP as the Source argument. Example:
	//   fooCommands := fooapp.NewCommands(fooRepo, auditRecorder, pool)
	auditRecorder := auditpkg.NewRecorder(auditRepo)
	_ = auditRecorder // remove underscore when Commands are added

	// vogel:component storage begin
	// S3-compatible storage (optional — only if STORAGE_CONFIGS is set).
	//
	// Usage in handlers:
	//   publicStore := storageReg.Get("public")
	//   privateStore := storageReg.Get("private")
	//   err := publicStore.Upload(ctx, &storage.UploadInput{Key: "assets/logo.png", Body: f, ContentType: "image/png"})
	//   // 0 expiry = the backend's PresignExpiry; the URL serves the object as an
	//   // attachment unless you pass storage.WithContentDisposition("inline").
	//   url, err := privateStore.PresignedGetURL(ctx, "docs/licencia.pdf", 0, storage.AsAttachment("licencia.pdf"))
	if len(cfg.Storages) > 0 {
		storageReg, err := s3.NewStorageRegistry(cfg.Storages, appLogger.Logger)
		if err != nil {
			return fmt.Errorf("initializing storage: %w", err)
		}
		_ = storageReg // remove underscore when handlers need storage
		appLogger.Info("storage initialized", "backends", storageReg.Names())
	}

	// vogel:component storage end
	// HTTP handlers
	healthHandler := handler.NewHealthHandler(pool, appLogger.Logger)
	auditHandler := handler.NewAuditHandler(auditRepo, appLogger.Logger)

	routerConfig := httpiface.RouterConfig{
		AppURL:      cfg.App.Url,
		Timeout:     cfg.App.Timeout,
		CORSOrigins: cfg.CORS.AllowedOrigins,

		TrustedProxies: cfg.App.TrustedProxies,

		// Swagger UI and spec only on an explicit SWAGGER_ENABLED=true
		// (never in production; config.Load rejects it there).
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

	router := httpiface.NewRouter(deps)

	docs.SwaggerInfo.Version = cfg.App.Version
	docs.SwaggerInfo.Host = cfg.App.Url
	docs.SwaggerInfo.BasePath = "/v1"

	server := httpiface.NewServer(cfg.App.Port, router, appLogger.Logger)

	appLogger.Info("server ready to accept connections", "port", cfg.App.Port)

	return server.Start(httpiface.MetricsDrainer(metricsSrv, appLogger.Logger))
}

// cerbosProbeTimeout bounds the startup reachability probe of the Cerbos PDP.
// cerbosProbeTimeout bounds each startup probe attempt; cerbosProbeBudget
// bounds all of them, so a PDP that is briefly down at boot (container start
// order, a Cerbos rollout) is waited for instead of killing the API, while a
// wrong host or TLS mode still fails the boot within seconds of the budget.
// Between attempts the wait starts at cerbosProbeBackoff and doubles, capped
// at cerbosProbeMaxBackoff. They are variables only so tests can shrink them.
var (
	cerbosProbeTimeout    = 5 * time.Second
	cerbosProbeBudget     = 30 * time.Second
	cerbosProbeBackoff    = 500 * time.Millisecond
	cerbosProbeMaxBackoff = 4 * time.Second
)

// pinger is the part of *cerbos.Checker the startup probe needs.
type pinger interface {
	Ping(ctx context.Context) error
}

// probeCerbos calls the PDP until it answers or cerbosProbeBudget is spent, so
// a wrong host or a TLS mismatch fails the boot instead of turning every
// authorization into a 503, and a PDP that is only briefly down is waited for.
// The gRPC connection is lazy, so cerbos.New alone proves nothing. The usual
// mismatch is a deployment written when plaintext was the default: Cerbos now
// uses TLS unless CERBOS_PLAINTEXT=true, which the error names.
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

// pingOnce runs one probe attempt bounded by cerbosProbeTimeout.
func pingOnce(ctx context.Context, p pinger) error {
	ctx, cancel := context.WithTimeout(ctx, cerbosProbeTimeout)
	defer cancel()
	return p.Ping(ctx)
}
