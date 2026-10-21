//go:build integration

package main

import (
	"context"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	auditmigrations "github.com/kafeiih/vogel/audit/migrations"

	// vogel:component bpms begin
	documentmigrations "github.com/kafeiih/vogel/document/migrations"
	workflowmigrations "github.com/kafeiih/vogel/workflow/migrations"

	// vogel:component bpms end
	// vogel:component worker begin
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/config"

	// vogel:component worker end
	"github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/database/migrations"
)

const appVersionTable = "goose_db_version"

const testDeadline = 2 * time.Minute

var revertEffects = map[string]func(ctx context.Context, t *testing.T, pool *pgxpool.Pool, applied bool){
	"001_enable_pgcrypto.sql": func(ctx context.Context, t *testing.T, pool *pgxpool.Pool, applied bool) {
		if got := extensionExists(ctx, t, pool, "pgcrypto"); got != applied {
			t.Errorf("pgcrypto installed = %t, want %t", got, applied)
		}
	},
}

// vogel:component worker begin
// vogel:component worker end
func TestMigrateDown_RevertsOnlyTheAppSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
	defer cancel()
	dbURL := startTestPostgres(ctx, t)
	t.Setenv("DB_URL", dbURL)
	t.Setenv("DB_REQUIRE_TLS", "false")

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// vogel:component worker begin
	riverSchema := config.WorkerSchema()
	// vogel:component worker end
	app := migrationVersions(t, migrations.FS)
	appLatest, appPrevious := app.latest(), app.previous()
	auditLatest := migrationVersions(t, auditmigrations.FS()).latest()
	// vogel:component bpms begin
	workflowLatest := migrationVersions(t, workflowmigrations.FS()).latest()
	documentLatest := migrationVersions(t, documentmigrations.FS()).latest()
	// vogel:component bpms end
	checkEffect, ok := revertEffects[app.latestFile()]
	if !ok {
		t.Fatalf("no revertEffects entry for the latest app migration %s: add one that checks its schema effect", app.latestFile())
	}

	runMigrateWithin(ctx, t, "up")

	if got := gooseVersion(ctx, t, pool, appVersionTable); got != appLatest {
		t.Fatalf("after up: app version = %d, want %d", got, appLatest)
	}
	if got := gooseVersion(ctx, t, pool, auditmigrations.DefaultTableName); got != auditLatest {
		t.Fatalf("after up: audit version = %d, want %d", got, auditLatest)
	}
	assertAuditLogIntact(ctx, t, pool, "after up")
	// vogel:component bpms begin
	if got := gooseVersion(ctx, t, pool, workflowmigrations.DefaultTableName); got != workflowLatest {
		t.Fatalf("after up: workflow version = %d, want %d", got, workflowLatest)
	}
	if got := gooseVersion(ctx, t, pool, documentmigrations.DefaultTableName); got != documentLatest {
		t.Fatalf("after up: document version = %d, want %d", got, documentLatest)
	}
	// vogel:component bpms end
	// vogel:component worker begin
	assertRiverTables(ctx, t, pool, riverSchema, "after up")
	riverVersion := riverMigrationVersion(ctx, t, pool, riverSchema)
	if riverVersion == 0 {
		t.Fatalf("after up: no River migration recorded in %s.river_migration", riverSchema)
	}
	// vogel:component worker end
	checkEffect(ctx, t, pool, true)
	if t.Failed() {
		t.FailNow()
	}

	runMigrateWithin(ctx, t, "down")

	if got := gooseVersion(ctx, t, pool, appVersionTable); got != appPrevious {
		t.Errorf("after down: app version = %d, want %d (exactly one reverted)", got, appPrevious)
	}
	checkEffect(ctx, t, pool, false)
	if got := gooseVersion(ctx, t, pool, auditmigrations.DefaultTableName); got != auditLatest {
		t.Errorf("after down: audit version = %d, want %d (unchanged)", got, auditLatest)
	}
	assertAuditLogIntact(ctx, t, pool, "after down")
	// vogel:component bpms begin
	if got := gooseVersion(ctx, t, pool, workflowmigrations.DefaultTableName); got != workflowLatest {
		t.Errorf("after down: workflow version = %d, want %d (unchanged)", got, workflowLatest)
	}
	if got := gooseVersion(ctx, t, pool, documentmigrations.DefaultTableName); got != documentLatest {
		t.Errorf("after down: document version = %d, want %d (unchanged)", got, documentLatest)
	}
	// vogel:component bpms end
	// vogel:component worker begin
	assertRiverTables(ctx, t, pool, riverSchema, "after down")
	if got := riverMigrationVersion(ctx, t, pool, riverSchema); got != riverVersion {
		t.Errorf("after down: River migration version = %d, want %d (unchanged)", got, riverVersion)
	}
	// vogel:component worker end
}

func startTestPostgres(ctx context.Context, t *testing.T) string {
	t.Helper()

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

func runMigrateWithin(ctx context.Context, t *testing.T, sub string) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- runMigrate([]string{sub}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("migrate %s: %v", sub, err)
		}
	case <-ctx.Done():
		t.Fatalf("migrate %s did not finish before the test deadline (%s for the whole test): %v", sub, testDeadline, ctx.Err())
	}
}

type versionedFiles []migrationFile

type migrationFile struct {
	version int64
	name    string
}

func migrationVersions(t *testing.T, fsys fs.FS) versionedFiles {
	t.Helper()
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("migration set has no .sql files")
	}
	set := make(versionedFiles, 0, len(names))
	for _, name := range names {
		prefix, _, found := strings.Cut(name, "_")
		v, err := strconv.ParseInt(prefix, 10, 64)
		if !found || err != nil {
			t.Fatalf("migration %s has no numeric version prefix", name)
		}
		set = append(set, migrationFile{version: v, name: name})
	}
	sort.Slice(set, func(i, j int) bool { return set[i].version < set[j].version })
	return set
}

func (s versionedFiles) latest() int64      { return s[len(s)-1].version }
func (s versionedFiles) latestFile() string { return s[len(s)-1].name }

func (s versionedFiles) previous() int64 {
	if len(s) < 2 {
		return 0
	}
	return s[len(s)-2].version
}

func gooseVersion(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table string) int64 {
	t.Helper()
	var v int64
	q := "SELECT COALESCE(MAX(version_id), 0) FROM " + pgx.Identifier{table}.Sanitize() + " WHERE is_applied"
	if err := pool.QueryRow(ctx, q).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	return v
}

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

// vogel:component worker begin
func assertRiverTables(ctx context.Context, t *testing.T, pool *pgxpool.Pool, schema, phase string) {
	t.Helper()
	for _, table := range []string{"river_migration", "river_job", "river_leader", "river_queue"} {
		name := pgx.Identifier{schema, table}.Sanitize()
		if !relationExists(ctx, t, pool, name) {
			t.Errorf("%s: River table %s missing", phase, name)
		}
	}
}

func riverMigrationVersion(ctx context.Context, t *testing.T, pool *pgxpool.Pool, schema string) int64 {
	t.Helper()
	var v int64
	q := "SELECT COALESCE(MAX(version), 0) FROM " + pgx.Identifier{schema, "river_migration"}.Sanitize()
	if err := pool.QueryRow(ctx, q).Scan(&v); err != nil {
		t.Fatalf("read River migration version: %v", err)
	}
	return v
}

// vogel:component worker end
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
