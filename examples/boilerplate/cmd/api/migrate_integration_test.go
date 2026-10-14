//go:build integration

package main

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	auditmigrations "github.com/kafeiih/vogel/audit/migrations"

	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/config"
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/database/migrations"
)

// appVersionTable is goose's default version table, used by the app set
// (migrationSets gives it no TableName).
const appVersionTable = "goose_db_version"

// TestMigrateDown_RevertsOnlyTheAppSet backs the claims the RUNBOOK rollback
// step makes about "./api migrate down": it reverts only the latest migration
// of this service's own set and never touches audit_log nor River. It drives
// runMigrate, the function main dispatches "api migrate ..." to, with DB_URL
// pointing at a throwaway Postgres.
//
// The advisory lock "migrate down" shares with "migrate up" is not re-tested
// here: vogel/migrate takes it on every operation, and
// migrate/runner_integration_test.go in the vogel module proves it.
func TestMigrateDown_RevertsOnlyTheAppSet(t *testing.T) {
	ctx := context.Background()
	dbURL := startTestPostgres(t)
	t.Setenv("DB_URL", dbURL)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	riverSchema := config.WorkerSchema()
	appLatest := latestVersion(t, migrations.FS)
	auditLatest := latestVersion(t, auditmigrations.FS())

	if err := runMigrate([]string{"up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	if got := gooseVersion(ctx, t, pool, appVersionTable); got != appLatest {
		t.Fatalf("after up: app version = %d, want %d", got, appLatest)
	}
	if got := gooseVersion(ctx, t, pool, auditmigrations.DefaultTableName); got != auditLatest {
		t.Fatalf("after up: audit version = %d, want %d", got, auditLatest)
	}
	assertAuditLogIntact(ctx, t, pool, "after up")
	assertRiverTables(ctx, t, pool, riverSchema, "after up")
	riverVersion := riverMigrationVersion(ctx, t, pool, riverSchema)
	if riverVersion == 0 {
		t.Fatalf("after up: no River migration recorded in %s.river_migration", riverSchema)
	}
	// The app set currently holds a single migration (001, pgcrypto), so
	// "down" takes it to zero; check its effect is applied before reverting.
	if !extensionExists(ctx, t, pool, "pgcrypto") {
		t.Fatal("after up: pgcrypto extension missing")
	}

	if err := runMigrate([]string{"down"}); err != nil {
		t.Fatalf("migrate down: %v", err)
	}

	if got := gooseVersion(ctx, t, pool, appVersionTable); got != appLatest-1 {
		t.Errorf("after down: app version = %d, want %d (exactly one reverted)", got, appLatest-1)
	}
	if appLatest == 1 && extensionExists(ctx, t, pool, "pgcrypto") {
		t.Error("after down: pgcrypto still installed; the app set's only migration was not reverted")
	}
	if got := gooseVersion(ctx, t, pool, auditmigrations.DefaultTableName); got != auditLatest {
		t.Errorf("after down: audit version = %d, want %d (unchanged)", got, auditLatest)
	}
	assertAuditLogIntact(ctx, t, pool, "after down")
	assertRiverTables(ctx, t, pool, riverSchema, "after down")
	if got := riverMigrationVersion(ctx, t, pool, riverSchema); got != riverVersion {
		t.Errorf("after down: River migration version = %d, want %d (unchanged)", got, riverVersion)
	}
}

// startTestPostgres spins up a postgres:17-alpine testcontainer and returns
// its connection string, the same way vogel's own integration tests do
// (workflow/postgres/repository_integration_test.go).
func startTestPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("test_db"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pgContainer.Terminate(context.Background()) })

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return connStr
}

// latestVersion returns the number of .sql migrations at the root of fsys.
// Each set numbers its files 001, 002, ... without gaps, so the count is the
// latest version.
func latestVersion(t *testing.T, fsys fs.FS) int64 {
	t.Helper()
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("migration set has no .sql files")
	}
	return int64(len(files))
}

// gooseVersion returns the highest applied version recorded in a goose
// version table (0 when only goose's bootstrap row is left).
func gooseVersion(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table string) int64 {
	t.Helper()
	var v int64
	q := "SELECT COALESCE(MAX(version_id), 0) FROM " + pgx.Identifier{table}.Sanitize() + " WHERE is_applied"
	if err := pool.QueryRow(ctx, q).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	return v
}

// assertAuditLogIntact checks audit_log exists with both append-only
// triggers from audit/migrations/002.
func assertAuditLogIntact(ctx context.Context, t *testing.T, pool *pgxpool.Pool, phase string) {
	t.Helper()
	if !relationExists(ctx, t, pool, "public.audit_log") {
		t.Fatalf("%s: audit_log missing", phase)
	}
	for _, trigger := range []string{"vogel_audit_log_no_update_delete", "vogel_audit_log_no_truncate"} {
		var n int
		err := pool.QueryRow(ctx,
			"SELECT count(*) FROM pg_trigger WHERE tgrelid = 'public.audit_log'::regclass AND tgname = $1",
			trigger).Scan(&n)
		if err != nil {
			t.Fatalf("%s: read pg_trigger: %v", phase, err)
		}
		if n != 1 {
			t.Errorf("%s: trigger %s on audit_log: found %d, want 1", phase, trigger, n)
		}
	}
}

// assertRiverTables checks River's core tables exist inside schema.
func assertRiverTables(ctx context.Context, t *testing.T, pool *pgxpool.Pool, schema, phase string) {
	t.Helper()
	for _, table := range []string{"river_migration", "river_job", "river_leader", "river_queue"} {
		name := pgx.Identifier{schema, table}.Sanitize()
		if !relationExists(ctx, t, pool, name) {
			t.Errorf("%s: River table %s missing", phase, name)
		}
	}
}

// riverMigrationVersion returns the highest River migration version applied
// in schema.
func riverMigrationVersion(ctx context.Context, t *testing.T, pool *pgxpool.Pool, schema string) int64 {
	t.Helper()
	var v int64
	q := "SELECT COALESCE(MAX(version), 0) FROM " + pgx.Identifier{schema, "river_migration"}.Sanitize()
	if err := pool.QueryRow(ctx, q).Scan(&v); err != nil {
		t.Fatalf("read River migration version: %v", err)
	}
	return v
}

func relationExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("to_regclass(%s): %v", name, err)
	}
	return exists
}

func extensionExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)", name).Scan(&exists); err != nil {
		t.Fatalf("read pg_extension: %v", err)
	}
	return exists
}
