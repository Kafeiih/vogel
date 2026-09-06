// Package migrate provides programmatic access to SQL/Go migrations owned by
// the consuming application, wrapping pressly/goose v3's Provider API.
//
// Unlike the source repositories' migrate package, this one does not embed or
// otherwise own any migration files: a shared library has no migrations of
// its own, only applications do. Every function here takes an fs.FS
// (typically an application's own go:embed'd directory, or os.DirFS during
// local development) as an explicit parameter instead of reading a
// package-level embedded default.
//
// Locking: every operation acquires a PostgreSQL session-level advisory lock
// (goose's default lock ID, a crc64 hash of "goose") for the duration of the
// migration run, via goose.WithSessionLocker. This is the fix this package
// adds over its source: none of the three source repositories locked at all,
// so two concurrent `migrate up` invocations — two CI jobs against the same
// database, two init containers racing on pod startup, a manual run
// overlapping a deploy pipeline — could interleave DDL and corrupt the
// goose_db_version bookkeeping instead of one simply waiting for the other.
//
// CREATE INDEX CONCURRENTLY: PostgreSQL does not allow CREATE INDEX
// CONCURRENTLY inside a transaction, and goose runs each migration in its own
// transaction by default. A migration file that needs it must start with the
// "-- +goose NO TRANSACTION" annotation (see goose's documentation for
// "-- +goose NO TRANSACTION") so goose runs it outside of a transaction
// instead of wrapping it in one that PostgreSQL will reject.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	// goose's Provider requires the database/sql driver. pgxpool uses pgx's
	// native protocol directly and cannot be passed to database/sql. Register
	// the pgx stdlib adapter here so sql.Open("pgx", dbURL) works throughout
	// this package.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// ErrNoMigrations is returned (via errors.Is) when fsys contains no migration
// files. Re-exported from goose so callers do not need to import goose
// themselves just to check this one error.
var ErrNoMigrations = goose.ErrNoMigrations

// Options configures optional behaviour for migrate operations.
type Options struct {
	// Logger is the slog logger used for goose output. When nil, slog.Default()
	// is used. Pass slog.New(slog.NewTextHandler(io.Discard, nil)) to silence
	// goose during tests.
	Logger *slog.Logger

	// LockID overrides the PostgreSQL advisory lock ID used to serialize
	// migration runs. Zero uses goose's default (a crc64 hash of "goose").
	// Override this only if a consumer needs distinct locks for distinct
	// migration sets running against the same database.
	LockID int64

	// DisableLock disables the session-level advisory lock. It exists as an
	// escape hatch for environments where advisory locks are unavailable
	// (e.g. a connection pooler in transaction-pooling mode that does not
	// preserve session state); leave it false in every normal deployment.
	DisableLock bool
}

// resolveLogger returns the first non-nil Logger from opts, or slog.Default().
func resolveLogger(opts []Options) *slog.Logger {
	for _, o := range opts {
		if o.Logger != nil {
			return o.Logger
		}
	}
	return slog.Default()
}

// resolveLockID returns the first non-zero LockID from opts, or 0 (meaning:
// let goose use its own default).
func resolveLockID(opts []Options) int64 {
	for _, o := range opts {
		if o.LockID != 0 {
			return o.LockID
		}
	}
	return 0
}

// lockDisabled reports whether any Options in opts disabled locking.
func lockDisabled(opts []Options) bool {
	for _, o := range opts {
		if o.DisableLock {
			return true
		}
	}
	return false
}

// openDB opens a *sql.DB using the pgx stdlib driver.
func openDB(dbURL string) (*sql.DB, error) {
	return sql.Open("pgx", dbURL)
}

// newProvider builds a goose.Provider over fsys, wired with the slog adapter
// and (unless disabled) the PostgreSQL session-level advisory locker.
//
// The returned *sql.DB is owned by the caller: closing the returned Provider
// via Close() also closes it.
func newProvider(dbURL string, fsys fs.FS, opts []Options) (*goose.Provider, error) {
	db, err := openDB(dbURL)
	if err != nil {
		return nil, err
	}

	logger := resolveLogger(opts)
	providerOpts := []goose.ProviderOption{goose.WithLogger(newSlogAdapter(logger))}

	if !lockDisabled(opts) {
		var lockerOpts []lock.SessionLockerOption
		if id := resolveLockID(opts); id != 0 {
			lockerOpts = append(lockerOpts, lock.WithLockID(id))
		}
		sessionLocker, err := lock.NewPostgresSessionLocker(lockerOpts...)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("creating session locker: %w", err)
		}
		providerOpts = append(providerOpts, goose.WithSessionLocker(sessionLocker))
	}

	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, providerOpts...)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return p, nil
}

// Up applies all pending migrations found in fsys. It is idempotent: calling
// Up on a fully migrated database is a no-op that returns nil.
//
// Returns an error if dbURL is unreachable, if fsys contains no migrations
// (ErrNoMigrations), or if any migration fails.
func Up(ctx context.Context, dbURL string, fsys fs.FS, opts ...Options) error {
	p, err := newProvider(dbURL, fsys, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	_, err = p.Up(ctx)
	return err
}

// Down reverts the most recently applied migration.
func Down(ctx context.Context, dbURL string, fsys fs.FS, opts ...Options) error {
	p, err := newProvider(dbURL, fsys, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	_, err = p.Down(ctx)
	return err
}

// UpTo applies all pending migrations up to and including the given version.
func UpTo(ctx context.Context, dbURL string, fsys fs.FS, version int64, opts ...Options) error {
	p, err := newProvider(dbURL, fsys, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	_, err = p.UpTo(ctx, version)
	return err
}

// UpByOne applies exactly one pending migration. Returns goose.ErrNoNextVersion
// if every migration has already been applied.
func UpByOne(ctx context.Context, dbURL string, fsys fs.FS, opts ...Options) error {
	p, err := newProvider(dbURL, fsys, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	_, err = p.UpByOne(ctx)
	return err
}

// Status writes a human-readable table of migration versions and their applied
// state to w.
func Status(ctx context.Context, dbURL string, fsys fs.FS, w io.Writer, opts ...Options) error {
	p, err := newProvider(dbURL, fsys, opts)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()

	statuses, err := p.Status(ctx)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintln(w, "VERSION\tSTATE\tAPPLIED AT\tSOURCE"); err != nil {
		return err
	}
	for _, s := range statuses {
		applied := "-"
		if !s.AppliedAt.IsZero() {
			applied = s.AppliedAt.Format(time.RFC3339)
		}
		path := ""
		version := int64(0)
		if s.Source != nil {
			path = s.Source.Path
			version = s.Source.Version
		}
		if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", version, s.State, applied, path); err != nil {
			return err
		}
	}
	return nil
}
