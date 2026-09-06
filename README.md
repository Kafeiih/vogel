# vogel

`vogel` is the shared Go foundation extracted from `go-bluprint`, the common ancestor
of `go-crucible` and `go-licencias`. It holds the packages that an audit of all three
repositories established as proven-shareable: pure ports/adapters and small utilities
with no business logic, intended to be consumed by both systems instead of duplicated.

Ports (interfaces) live in dependency-free packages; adapters live in subpackages, so
importing a port never pulls in the AWS SDK, SendGrid, or go-mail transitively.

## Package map

| Package | What it is |
|---|---|
| `stringutil` | Flexible date parsing (`ParseFlexibleDate`) and accent/whitespace string normalization (`Normalize`). |
| `logger` | A thin `*slog.Logger` wrapper (`Logger`). Carries a request/correlation ID via its own context key (`WithRequestID` / `RequestIDFromContext`) — it does not depend on any HTTP router. |
| `storage` | The `Storage` port: `Upload`, `Download`, `Delete`, `PresignedGetURL`, `PresignedPutURL`. No dependency beyond the standard library. |
| `storage/s3` | S3-compatible adapter (`S3Storage`) implementing `storage.Storage` via AWS SDK v2. Works with AWS S3, MinIO, DigitalOcean Spaces, and other S3-compatible services. Includes `StorageRegistry` for holding multiple named storages. |
| `notification` | The `Notifier` port: `Send(ctx, *Message) error`, plus `Message.Validate()`. No dependency beyond the standard library. |
| `notification/smtp` | SMTP adapter (`SMTPNotifier`) via `go-mail`. Dials a fresh connection per `Send` — see the doc comment on `SMTPNotifier` for why. |
| `notification/sendgrid` | SendGrid adapter (`SendGridNotifier`) via the SendGrid HTTP API. |
| `httpx/response` | Standard JSON success/error/list response envelopes (`response.JSON`, `response.Error`, `response.ValidationError`, `response.JSONList`, ...). |
| `httpx/middleware` | HTTP middleware: `Recovery`, `RateLimitJSON`, `RequestInfoMiddleware`, `SecurityHeaders`, `StructuredLogger`, `Metrics` (Prometheus), and `LoggerRequestID` (bridges a router's request ID into the `logger` package's context key). |
| `pgxtx` | pgx transaction plumbing: `PgxTxManager.WithTx`, `DBFromContext`, `TxFromContext`, the `DBTX` interface. Named `pgxtx` (not `repository`) because it holds transaction plumbing, not repositories. Lives at the module root, as a sibling of `postgres`, because `WithTx` works against any `*pgxpool.Pool` the consumer built themselves. |
| `request` | HTTP request helpers: `JSON` / `JSONWithLimit` (size-limited, unknown-field-rejecting JSON decoding with mapped 400/413 responses) and `Validator` (accumulates per-field validation errors for query params and chi URL params: `UUIDParam`, `IntQuery`, `TimeQuery`, `DateQuery`, `Enum`, `PublicIDParam`, ...). |
| `config` | Primitives for reading and validating environment variables: typed readers with defaults (`String`, `Bool`, `Int`, `Int32`, `Duration`, `StringSlice`), a required-variable reader (`Require`), an `Errors` accumulator so a boot failure reports every problem at once, and semantic validators (`ValidURL`, `IntRange`, `OneOf`, `MinMax`). Deliberately does **not** define application config structs — those stay in the consuming app. |
| `postgres` | `NewPool(ctx, Config, *slog.Logger) (*pgxpool.Pool, error)`: a pgxpool constructor with slow-query tracing (`SlowQueryTracer`), Prometheus pool metrics (`PoolMetricsCollector`), server-side `statement_timeout` / `lock_timeout` / `idle_in_transaction_session_timeout` (configurable, sane defaults), and a hard startup error when `RequireTLS` is set but the DSN disables TLS. |
| `migrate` | Programmatic access to goose v3 migrations owned by the *consuming application* (this package embeds none of its own): `Up`, `Down`, `UpTo`, `UpByOne`, `Status`, all taking an explicit `fs.FS`. Every run acquires a PostgreSQL session-level advisory lock via `goose.WithSessionLocker` so concurrent `migrate up` invocations serialize instead of racing. |

## Fixes applied during extraction

The source packages had nine known issues; all were fixed as part of this port rather
than carried forward:

1. **`logger` no longer imports chi.** The original `pkg/logger` read the request ID
   directly from `github.com/go-chi/chi/v5/middleware`, coupling a logging package to
   an HTTP router. `logger` now owns its own context key (`WithRequestID` /
   `RequestIDFromContext`); `httpx/middleware.LoggerRequestID` is the bridge that reads
   chi's request ID and feeds it into that key. `Logger.WithContext` was reworked to
   use this key instead of being dead code.
2. **`pgxtx.PgxTxManager.WithTx` is panic-safe.** A panic inside the callback now rolls
   back the transaction (via a `defer`/`recover`) before re-panicking, instead of
   leaking an open transaction until pool eviction. Covered by
   `TestPgxTxManager_WithTx_PanicInCallback_RollsBackAndPropagates`.
3. **`notification/smtp` documents its actual behavior instead of contradicting it.**
   The original ADR claimed a "connection pool"; the code dialed a fresh SMTP
   connection on every `Send`. This port keeps dial-per-send (simpler, always
   correct, no shared-client concurrency concerns) and states so explicitly in the
   `SMTPNotifier` doc comment, including a pointer to what a pooled implementation
   would need if send volume ever justifies it.
4. **`httpx/middleware.Metrics` no longer panics on double registration.** The
   original called `prometheus.MustRegister` in `init()`, which panics if constructed
   more than once. It is now `NewMetrics(reg prometheus.Registerer) (*Metrics, error)`
   (nil `reg` defaults to `prometheus.DefaultRegisterer`), which treats
   `AlreadyRegisteredError` as success by reusing the existing collector. Covered by
   `TestNewMetrics_DoubleConstruction_DoesNotPanic`.
5. **`request.JSON`'s byte limit was off by 2.** All three source repositories wrote
   `maxBytes := 1_048_578 // 1mb`; the real value of 1 MiB is `1_048_576`, and the typo
   had already been copied to a second call site in one repository. Fixed to
   `1_048_576` (`request.JSONWithLimit` also lets callers override the limit per
   endpoint). Covered by `TestJSON_DefaultLimitIsExactly1MiB`.
6. **`config` gained semantic validation, not just presence checks.** The source
   `config.Load()` only ever checked "is this env var non-empty" — a typo in
   `NOTIFICATION_PROVIDER` (e.g. `sendgrdi`) silently fell through to the SMTP default
   and disabled email delivery, with nothing but a `Warn` log to notice by. `config`
   adds `ValidURL`, `IntRange`, `OneOf`, and `MinMax`, plus an `Errors` accumulator so a
   boot failure reports every missing/invalid variable at once instead of one per
   restart.
7. **`postgres.NewPool` fails fast when `RequireTLS` can't be honored.** The source
   `applyTLSConfig` only raised the minimum TLS version *if TLS was already enabled by
   the DSN*; a DSN with `sslmode=disable` silently produced a plaintext connection
   while the caller believed `RequireTLS` had taken effect — the source code even
   documented the gap without closing it. Now returns a startup error naming the
   offending setting instead. Covered by
   `TestApplyTLSConfig_RequireTLSTrueWithDisabledDSN_FailsFast` and an integration test
   proving the positive path still preserves `ServerName` (see `postgres_test.go`).
8. **`postgres` adds the three server-side timeouts none of the three repositories
   set**: `statement_timeout`, `lock_timeout`, and `idle_in_transaction_session_timeout`,
   applied via `pgxpool`'s `AfterConnect` hook with configurable, sane defaults.
   `idle_in_transaction_session_timeout` matters most: it is the only server-side
   backstop against a handler that hangs while holding an open transaction and its
   locks — `statement_timeout` does not help there because no statement is running.
   Verified against a real container in `TestNewPool_AppliesServerSideTimeouts`.
9. **`postgres.PoolMetricsCollector` no longer panics on double registration** — same
   class of bug and same fix shape as `httpx/middleware.Metrics` (item 4):
   `NewPoolMetricsCollector(pool, reg)` now returns `(*PoolMetricsCollector, error)` and
   reuses an already-registered collector instead of calling `MustRegister`. Covered by
   `TestNewPoolMetricsCollector_DoubleConstruction_DoesNotPanic`.
10. **`migrate` now locks.** None of the three repositories' migration runners took any
    lock, so two concurrent `migrate up` invocations (two CI jobs, two init containers,
    a manual run overlapping a pipeline) could interleave DDL against the same
    database. `migrate` now builds its `goose.Provider` with
    `goose.WithSessionLocker` (a PostgreSQL session-level advisory lock), serializing
    concurrent runs instead of racing. Verified against a real container with 5
    concurrent `migrate.Up` calls in `TestUp_ConcurrentInvocations_SerializeInsteadOfRacing`.
    A migration needing `CREATE INDEX CONCURRENTLY` still must use goose's
    `-- +goose NO TRANSACTION` annotation, since PostgreSQL rejects that statement
    inside a transaction — see the doc comment on `migrate/runner.go`.

## Not here yet

This is a second slice. Deliberately excluded, pending decisions:

- **`auth`, `authz`** — blocked on a pending tenancy decision.
- **`audit`** — blocked on a dependency-inversion fix in the source
  (`application/audit/recorder.go` imports `interfaces/http/middleware`, the wrong
  direction for a port) and a decision on whether the library or the consuming
  application owns the `audit_log` table schema.
- **The `/v1` URL prefix / `router.go`** — blocked on convergence decisions between the
  two systems about routing conventions.
- **`worker`, `cmd/`, migrations (the SQL files themselves), Dockerfile,
  docker-compose, swagger** — out of scope for a shared library slice; these are
  per-application concerns, not shareable ports/adapters. `migrate/create.go` (a
  migration-file scaffolder) was likewise left out as a CLI/template concern, not
  library API.
- **`null_helpers.go`** (`nullInt`/`nullInt64`) — left behind deliberately. It returns
  `nil` when `n == 0`, conflating "unset" with a legitimate zero value. It was also
  unused in all three source repositories.
