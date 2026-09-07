// Package postgres provides a pgxpool.Pool constructor with the operational
// defaults the source applications lacked: slow-query tracing (see
// slow_query_tracer.go), Prometheus pool metrics (see metrics.go), a hard
// startup failure instead of a silent no-op when TLS is required but the DSN
// does not enable it, and server-side statement/lock/idle-in-transaction
// timeouts applied on every new connection.
package postgres

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const slowQueryThreshold = 200 * time.Millisecond

// Sane defaults for the server-side timeouts Config does not set explicitly.
// See the doc comments on Config's timeout fields for why each one matters.
const (
	defaultStatementTimeout                = 30 * time.Second
	defaultLockTimeout                     = 5 * time.Second
	defaultIdleInTransactionSessionTimeout = 60 * time.Second
)

// Config configures NewPool. Adapters own their Config struct rather than
// reading a global application config, so this type carries only what a
// pgxpool.Pool needs — the consuming application decides how (env vars, a
// flags package, hardcoded test values, ...) to fill it in.
type Config struct {
	// URL is the PostgreSQL connection string (DSN), e.g.
	// "postgres://user:pass@host:5432/db?sslmode=require".
	URL string

	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
	// ^ Pool sizing and connection lifetimes. Every one of these fields is
	// optional: zero means "keep whatever pgxpool.ParseConfig derived from
	// the DSN and its own defaults", never "zero connections" or "expire
	// immediately". See applyPoolSettings for why that distinction is not
	// cosmetic.

	// RequireTLS enforces a minimum TLS 1.2 connection. If true and the parsed
	// DSN does not enable TLS (e.g. sslmode=disable), NewPool fails fast with
	// an error instead of silently opening a plaintext connection. See
	// applyTLSConfig for the historical bug this fixes.
	RequireTLS bool

	// StatementTimeout caps how long a single SQL statement may run before
	// PostgreSQL cancels it (server-side `SET statement_timeout`). Protects
	// against a runaway query holding a connection (and any locks it took)
	// indefinitely. Zero uses defaultStatementTimeout (30s); a negative value
	// disables the timeout explicitly.
	StatementTimeout time.Duration

	// LockTimeout caps how long a statement will wait to acquire a row/table
	// lock before failing (server-side `SET lock_timeout`). Protects against a
	// request queuing behind a lock held by another slow transaction instead
	// of failing fast and letting the caller retry or surface an error. Zero
	// uses defaultLockTimeout (5s); a negative value disables it explicitly.
	LockTimeout time.Duration

	// IdleInTransactionSessionTimeout caps how long a connection may sit idle
	// while inside an open transaction (server-side
	// `SET idle_in_transaction_session_timeout`) before PostgreSQL kills the
	// session.
	//
	// This is the timeout that matters most of the three: it is the only
	// server-side backstop against a handler bug that begins a transaction,
	// then hangs (a stuck downstream call, a forgotten context deadline, a
	// panic recovered above the DB layer without rolling back) while still
	// holding the transaction's row locks. Without it, that one stuck request
	// can block every other request that needs the same rows — potentially
	// the whole pool, if MaxConns is small — until the process is restarted.
	// statement_timeout does not help here because no statement is running;
	// the connection is simply idle with an open transaction.
	//
	// Zero uses defaultIdleInTransactionSessionTimeout (60s); a negative value
	// disables it explicitly.
	IdleInTransactionSessionTimeout time.Duration

	// AfterConnect, if set, runs after this package's own per-connection setup
	// (TLS, timeouts, tracer) on every new physical connection. Use it for
	// consumer-specific per-connection work — for example, registering a
	// custom pgtype codec such as jackc/pgx-shopspring-decimal, the way
	// go-crucible's original constructor did inline. Returning an error here
	// fails that connection attempt.
	AfterConnect func(ctx context.Context, conn *pgx.Conn) error
}

// NewPool creates and validates a *pgxpool.Pool from cfg. It fails fast (before
// returning a pool) if the DSN is invalid, if RequireTLS is true but the DSN
// does not enable TLS, or if the initial ping fails.
func NewPool(ctx context.Context, cfg Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing database config: %w", err)
	}

	applyPoolSettings(poolConfig, cfg)
	poolConfig.HealthCheckPeriod = 1 * time.Minute

	if err := applyTLSConfig(poolConfig, cfg.RequireTLS); err != nil {
		return nil, err
	}

	poolConfig.ConnConfig.Tracer = NewSlowQueryTracer(logger, slowQueryThreshold)

	statementTimeoutMS := resolveTimeoutMillis(cfg.StatementTimeout, defaultStatementTimeout)
	lockTimeoutMS := resolveTimeoutMillis(cfg.LockTimeout, defaultLockTimeout)
	idleTimeoutMS := resolveTimeoutMillis(cfg.IdleInTransactionSessionTimeout, defaultIdleInTransactionSessionTimeout)
	userAfterConnect := cfg.AfterConnect

	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if _, err := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = %d", statementTimeoutMS)); err != nil {
			return fmt.Errorf("setting statement_timeout: %w", err)
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", lockTimeoutMS)); err != nil {
			return fmt.Errorf("setting lock_timeout: %w", err)
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf("SET idle_in_transaction_session_timeout = %d", idleTimeoutMS)); err != nil {
			return fmt.Errorf("setting idle_in_transaction_session_timeout: %w", err)
		}
		if userAfterConnect != nil {
			return userAfterConnect(ctx, conn)
		}
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}

// applyPoolSettings copies cfg's pool sizing and connection lifetime fields
// onto poolConfig, skipping every field left at its zero value.
//
// Skipping is the whole point. pgxpool.ParseConfig has already filled these
// in with usable defaults (a one hour MaxConnLifetime, a thirty minute
// MaxConnIdleTime, MaxConns of at least 4), and pgxpool reads a zero
// MaxConnLifetime as "this connection expired the moment it was created" —
// not as "no limit". Writing an unset field straight through therefore
// produced a pool that destroyed every connection on acquire and failed its
// very first Ping with pgxpool's opaque "too many failed attempts acquiring
// connection; likely bug in PrepareConn, BeforeAcquire, or ShouldPing hook",
// which names three hooks that had nothing to do with it.
//
// So the zero value of every field here means "keep pgxpool's default",
// which is the convention ConnectTimeout and the three server-side timeouts
// already followed.
func applyPoolSettings(poolConfig *pgxpool.Config, cfg Config) {
	if cfg.MaxConns > 0 {
		poolConfig.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolConfig.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolConfig.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		poolConfig.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.ConnectTimeout > 0 {
		poolConfig.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}
}

// resolveTimeoutMillis applies the zero-means-default / negative-means-disabled
// convention documented on Config's timeout fields, and converts to the
// integer milliseconds PostgreSQL's SET statements expect.
func resolveTimeoutMillis(configured, defaultValue time.Duration) int64 {
	switch {
	case configured < 0:
		return 0
	case configured == 0:
		return defaultValue.Milliseconds()
	default:
		return configured.Milliseconds()
	}
}

// applyTLSConfig enforces a minimum TLS 1.2 when requireTLS is true, while
// preserving the ServerName and other fields that pgxpool.ParseConfig sets
// from the DSN — replacing TLSConfig wholesale would drop ServerName, breaking
// certificate validation against managed databases like RDS.
//
// If requireTLS is true and the DSN disables TLS (e.g. sslmode=disable), pgx
// has already set ConnConfig.TLSConfig to nil by the time this runs, and
// pgconn never attempts a TLS handshake in that case regardless of what this
// function mutates afterward — the source repositories mutated a TLSConfig
// object anyway, silently producing a plaintext connection while callers
// believed RequireTLS had taken effect. This returns an error instead, naming
// the offending setting, so a misconfigured DSN fails at startup rather than
// shipping an unencrypted production connection.
func applyTLSConfig(poolConfig *pgxpool.Config, requireTLS bool) error {
	if !requireTLS {
		return nil
	}
	if poolConfig.ConnConfig.TLSConfig == nil {
		return fmt.Errorf(
			"postgres: RequireTLS is true but the connection string disables TLS " +
				"(e.g. sslmode=disable); set sslmode=require (or stronger) in the DSN, or set RequireTLS to false",
		)
	}
	poolConfig.ConnConfig.TLSConfig.MinVersion = tls.VersionTLS12
	return nil
}
