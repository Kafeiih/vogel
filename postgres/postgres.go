// Package postgres provides a pgxpool.Pool constructor with the operational
// defaults the source applications lacked: slow-query tracing (see
// slow_query_tracer.go), an optional acquire timeout, acquire-wait and
// connection-hold histograms with optional leak detection (see
// pool_tracer.go), Prometheus pool metrics (see metrics.go), a hard
// startup failure instead of a silent no-op when TLS is required but the DSN
// does not enable a verified TLS connection, and server-side statement/lock/idle-in-transaction
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
	// "postgres://user:pass@host:5432/db?sslmode=verify-full".
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

	// RequireTLS demands an encrypted connection to a verified server, with a
	// minimum of TLS 1.2. If true, NewPool fails fast unless the DSN makes pgx
	// verify the server certificate for every host it may connect to:
	// sslmode=verify-full (recommended: chain and hostname), verify-ca
	// (chain only), or require with sslrootcert (which libpq and pgx treat
	// as verify-ca). disable, allow, prefer (the default when sslmode is
	// absent) and a bare require are rejected: they either permit plaintext
	// or accept any certificate, so a man in the middle could read the
	// traffic. False takes the DSN as is. See applyTLSConfig.
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

	// AcquireTimeout bounds how long Acquire (and every Pool method that
	// acquires: Exec, Query, QueryRow, Begin, ...) waits for a free pool
	// connection, so a full pool makes requests fail fast instead of queuing
	// until the caller's own deadline. It applies through pgxpool's
	// AcquireTracer: the acquire context gets this deadline (a shorter caller
	// deadline still wins) and is canceled when Acquire returns.
	//
	// A wait cut by it fails with an error wrapping context.DeadlineExceeded:
	// recognize it with errors.Is(err, context.DeadlineExceeded) while the
	// caller's own ctx.Err() is still nil (the caller's deadline produces the
	// same error, but then ctx.Err() is set too). The wait is observed in the
	// db_pool_acquire_wait_seconds histogram either way.
	//
	// The deadline covers the whole acquire, so when the pool has room but no
	// idle connection it also bounds dialing a new one (as HikariCP's
	// connectionTimeout does): keep it above the time a healthy connect
	// takes, or a request that has to open a connection fails before
	// ConnectTimeout. NewPool's own startup Ping is exempt, so the boot still
	// waits up to ConnectTimeout for a database that is waking up.
	//
	// Zero disables it (pgxpool's behavior: wait for the caller's context); a
	// negative value is a NewPool error.
	AcquireTimeout time.Duration

	// LeakThreshold makes NewPool log a WARN when a connection is released
	// after being held (acquired and not yet released) longer than this,
	// naming the function and file:line that acquired it: the first frame
	// outside pgx and this module's postgres and pgxtx packages. The WARN
	// never includes SQL or arguments. Hold times are always observed in the
	// db_pool_conn_hold_seconds histogram; this only adds the log and the
	// per-acquire stack capture it needs, so leave it zero when unused.
	//
	// The check runs at release, so a connection that is never released is
	// not reported by this log; db_pool_connections_acquired staying at
	// db_pool_connections_max shows that case. Zero disables it; a negative
	// value is a NewPool error.
	LeakThreshold time.Duration

	// HashSlowQuerySQL makes the slow-query WARN log sql_hash (a short SHA-256
	// prefix of the SQL text) instead of the SQL text. Leave it false to see
	// the statement, cut at 2 KiB. Bind values are never logged. The hash is
	// unsalted: it keeps the statement text out of the logs, but a reader who
	// knows the template can still confirm a low-entropy inlined literal by
	// hashing guesses, so bind such values as parameters instead. In this mode
	// a failed query's error text is dropped too, since it can quote a value:
	// a PostgreSQL error is logged by SQLSTATE (error_code) and constraint
	// name, any other error by its Go type.
	HashSlowQuerySQL bool

	// Tracer, if set, is called alongside the tracer NewPool always installs
	// (slow queries, acquire timeout, pool histograms, leak detection), on
	// every hook it implements: pgx.QueryTracer, and optionally
	// pgx.BatchTracer, pgx.CopyFromTracer, pgx.PrepareTracer,
	// pgx.ConnectTracer, pgxpool.AcquireTracer and pgxpool.ReleaseTracer.
	// Its Start hooks run before vogel's and its End hooks after, so a span
	// it opens encloses the slow-query log line. Any OpenTelemetry pgx
	// tracer fits it; this package depends on no tracing library.
	Tracer pgx.QueryTracer

	// TraceIDs, if set, returns the trace and span IDs of the span carried
	// by a context, or empty strings when there is none. The pool then
	// attaches the acquiring span as an exemplar to db_pool_acquire_wait_seconds
	// and db_pool_conn_hold_seconds, and adds trace_id and span_id to the
	// leak-threshold warning (read at acquire: the release has no context).
	// otel.SampledTraceIDs fits it, so exemplars only point at exported
	// traces. Prometheus reads exemplars only in the OpenMetrics format.
	TraceIDs func(ctx context.Context) (traceID, spanID string)

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
// does not make pgx verify the server certificate, or if the initial ping
// fails.
func NewPool(ctx context.Context, cfg Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := newPoolConfig(cfg, logger)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(withoutAcquireTimeout(ctx)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}

// newPoolConfig builds the *pgxpool.Config NewPool connects with, without
// connecting: DSN parsing, pool settings, TLS policy, the pool tracer and the
// per-connection setup.
func newPoolConfig(cfg Config, logger *slog.Logger) (*pgxpool.Config, error) {
	if cfg.AcquireTimeout < 0 {
		return nil, fmt.Errorf("postgres: AcquireTimeout must not be negative, got %s (zero disables it)", cfg.AcquireTimeout)
	}
	if cfg.LeakThreshold < 0 {
		return nil, fmt.Errorf("postgres: LeakThreshold must not be negative, got %s (zero disables it)", cfg.LeakThreshold)
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing database config: %w", err)
	}

	applyPoolSettings(poolConfig, cfg)
	poolConfig.HealthCheckPeriod = 1 * time.Minute

	if err := applyTLSConfig(poolConfig, cfg.RequireTLS); err != nil {
		return nil, err
	}

	poolConfig.ConnConfig.Tracer = newPoolTracer(poolTracerOptions{
		logger:             logger,
		slowQueryThreshold: slowQueryThreshold,
		hashSlowQuerySQL:   cfg.HashSlowQuerySQL,
		extra:              cfg.Tracer,
		traceIDs:           cfg.TraceIDs,
		acquireTimeout:     cfg.AcquireTimeout,
		leakThreshold:      cfg.LeakThreshold,
		maxConns:           int(poolConfig.MaxConns),
	})

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

	return poolConfig, nil
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

// CheckTLS reports whether dsn satisfies RequireTLS, applying exactly the
// check NewPool applies, without connecting. It is for code that must hand a
// DSN string to something other than NewPool -- a migration tool, for
// instance -- and still honor the same TLS policy as the rest of the service.
// It returns an error for an unparsable DSN, or one that allows a plaintext
// connection or does not verify the server certificate.
func CheckTLS(dsn string) error {
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parsing database config: %w", err)
	}
	return applyTLSConfig(poolConfig, true)
}

// applyTLSConfig enforces RequireTLS: every TLS configuration pgx may use
// (the primary host and every fallback) must exist and verify the server,
// and each gets a minimum of TLS 1.2. It mutates the configs pgxpool.ParseConfig
// built from the DSN rather than replacing them, because replacing TLSConfig
// wholesale would drop ServerName and the verification settings, breaking
// certificate validation against managed databases like RDS.
//
// The check reads the parsed configuration instead of the sslmode string, so
// it also covers keyword/value DSNs, PGSSLMODE and multi-host DSNs. A nil
// TLSConfig means pgx connects in plaintext for that attempt (disable, or the
// plaintext leg of allow/prefer); InsecureSkipVerify without a
// VerifyPeerCertificate callback means any certificate is accepted (prefer,
// allow's TLS leg, and require without sslrootcert). verify-ca sets
// InsecureSkipVerify too, but only to replace Go's hostname check with its own
// chain verification in VerifyPeerCertificate.
//
// Historically the source repositories mutated a TLSConfig object even when
// the DSN disabled TLS, silently producing a plaintext connection while
// callers believed RequireTLS had taken effect. Failing at startup with an
// error naming the setting keeps a misconfigured DSN out of production.
func applyTLSConfig(poolConfig *pgxpool.Config, requireTLS bool) error {
	if !requireTLS {
		return nil
	}

	configs := []*tls.Config{poolConfig.ConnConfig.TLSConfig}
	for _, fb := range poolConfig.ConnConfig.Fallbacks {
		configs = append(configs, fb.TLSConfig)
	}

	for _, c := range configs {
		if c == nil {
			return fmt.Errorf(
				"postgres: RequireTLS is true but the connection string allows a plaintext connection " +
					"(sslmode disable, allow or prefer, or sslmode absent); set sslmode=verify-full in the DSN, or set RequireTLS to false",
			)
		}
		if c.InsecureSkipVerify && c.VerifyPeerCertificate == nil {
			return fmt.Errorf(
				"postgres: RequireTLS is true but the connection string does not verify the server certificate " +
					"(sslmode=require without sslrootcert, or prefer/allow); set sslmode=verify-full (or verify-ca with sslrootcert) in the DSN, or set RequireTLS to false",
			)
		}
	}

	for _, c := range configs {
		c.MinVersion = tls.VersionTLS12
	}
	return nil
}
