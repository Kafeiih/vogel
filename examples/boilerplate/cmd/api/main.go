package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	auditpkg "github.com/kafeiih/vogel/audit"
	auditmigrations "github.com/kafeiih/vogel/audit/migrations"
	auditpg "github.com/kafeiih/vogel/audit/postgres"
	"github.com/kafeiih/vogel/auth/zitadel"
	"github.com/kafeiih/vogel/authz/cerbos"
	vmw "github.com/kafeiih/vogel/httpx/middleware"
	"github.com/kafeiih/vogel/logger"
	"github.com/kafeiih/vogel/migrate"
	"github.com/kafeiih/vogel/postgres"
	"github.com/kafeiih/vogel/storage/s3"

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
	switch subcmd {
	case "", "server":
		if err := runServer(); err != nil {
			log.Fatal(err)
		}
	case "migrate":
		if err := runMigrate(args[1:]); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q; valid: server, migrate\n", subcmd)
		os.Exit(1)
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

	ctx := context.Background()

	switch subcmd {
	case "up":
		return migrate.Up(ctx, dbURL, migrations.FS)
	case "down":
		return migrate.Down(ctx, dbURL, migrations.FS)
	case "status":
		return migrate.Status(ctx, dbURL, migrations.FS, os.Stdout)
	default:
		if subcmd == "" {
			return fmt.Errorf("migrate subcommand required; valid: up, down, status, create")
		}
		return fmt.Errorf("unknown migrate subcommand %q; valid: up, down, status, create", subcmd)
	}
}

func runServer() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	appLogger := logger.NewDefault(cfg.App.Env)
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
	if err := migrate.Up(ctx, cfg.Database.URL, migrations.FS, migrate.Options{Logger: appLogger.Logger}); err != nil {
		return fmt.Errorf("running app migrations: %w", err)
	}
	if err := migrate.Up(ctx, cfg.Database.URL, auditmigrations.FS(), migrate.Options{
		Logger:    appLogger.Logger,
		TableName: auditmigrations.DefaultTableName,
	}); err != nil {
		return fmt.Errorf("running audit migrations: %w", err)
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

	appLogger.Info("zitadel authenticator initialized")

	// Cerbos authorization (PDP via gRPC)
	cerbosChecker, err := cerbos.New(cfg.Cerbos)
	if err != nil {
		return fmt.Errorf("initializing cerbos: %w", err)
	}
	defer func() { _ = cerbosChecker.Close() }()

	appLogger.Info("cerbos checker initialized", "host", cfg.Cerbos.Host)

	// Audit subsystem
	auditRepo := auditpg.NewRepository(pool)

	// auditRecorder is passed to command handlers that need to record
	// mutations, with audit.SourceHTTP as the Source argument. Example:
	//   fooCommands := fooapp.NewCommands(fooRepo, auditRecorder, pool)
	auditRecorder := auditpkg.NewRecorder(auditRepo)
	_ = auditRecorder // remove underscore when Commands are added

	// S3-compatible storage (optional — only if STORAGE_CONFIGS is set).
	//
	// Usage in handlers:
	//   publicStore := storageReg.Get("public")
	//   privateStore := storageReg.Get("private")
	//   err := publicStore.Upload(ctx, &storage.UploadInput{Key: "assets/logo.png", Body: f, ContentType: "image/png"})
	//   url, err := privateStore.PresignedGetURL(ctx, "docs/licencia.pdf", 15*time.Minute)
	if len(cfg.Storages) > 0 {
		storageReg, err := s3.NewStorageRegistry(cfg.Storages, appLogger.Logger)
		if err != nil {
			return fmt.Errorf("initializing storage: %w", err)
		}
		_ = storageReg // remove underscore when handlers need storage
		appLogger.Info("storage initialized", "backends", storageReg.Names())
	}

	// HTTP handlers
	healthHandler := handler.NewHealthHandler(pool)
	auditHandler := handler.NewAuditHandler(auditRepo, appLogger.Logger)

	routerConfig := httpiface.RouterConfig{
		AppURL:      cfg.App.Url,
		Timeout:     cfg.App.Timeout,
		CORSOrigins: cfg.CORS.AllowedOrigins,
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
