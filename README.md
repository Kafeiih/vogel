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
| `reqctx` | Request-scoped metadata carried through `context.Context`, independent of any HTTP router or logging framework: a request/correlation ID (`WithRequestID`/`RequestIDFromContext`) and `RequestInfo` — client IP and User-Agent (`WithRequestInfo`/`RequestInfoFromContext`). No dependency beyond the standard library's `context` package. The neutral ground between `httpx/middleware` (the writer) and `logger`/`audit` (the readers), so neither has to import the other. |
| `logger` | A thin `*slog.Logger` wrapper (`Logger`). Reads the request/correlation ID from `reqctx` (via `Logger.WithContext`) — it does not depend on any HTTP router, and does not own its own request-ID context key (that ownership moved to `reqctx` — see fix 16 below). |
| `storage` | The `Storage` port: `Upload`, `Download`, `Delete`, `PresignedGetURL`, `PresignedPutURL`. No dependency beyond the standard library. |
| `storage/s3` | S3-compatible adapter (`S3Storage`) implementing `storage.Storage` via AWS SDK v2. Works with AWS S3, MinIO, DigitalOcean Spaces, and other S3-compatible services. Includes `StorageRegistry` for holding multiple named storages. |
| `notification` | The `Notifier` port: `Send(ctx, *Message) error`, plus `Message.Validate()`. No dependency beyond the standard library. |
| `notification/smtp` | SMTP adapter (`SMTPNotifier`) via `go-mail`. Dials a fresh connection per `Send` — see the doc comment on `SMTPNotifier` for why. |
| `notification/sendgrid` | SendGrid adapter (`SendGridNotifier`) via the SendGrid HTTP API. |
| `httpx/response` | Standard JSON success/error/list response envelopes (`response.JSON`, `response.Error`, `response.ValidationError`, `response.JSONList`, ...). |
| `httpx/middleware` | HTTP middleware: `Recovery`, `RateLimitJSON`, `RequestContext` (the single writer that populates the request ID, client IP, and User-Agent into `reqctx` for every inbound request — replaces the old `RequestInfoMiddleware` + `LoggerRequestID` pair), `SecurityHeaders`, `StructuredLogger`, `Metrics` (Prometheus), `Authenticate` (runs an `auth.Authenticator` and stores the resulting principal in the request context), and `RequirePermission` (runs an `authz.Checker` against the authenticated principal). |
| `auth` | The `Authenticator` port: `Authenticate(ctx, *http.Request) (*Principal, error)`. Context helpers (`WithPrincipal`/`FromContext`) and typed sentinel errors (`ErrUnauthenticated`, `ErrForbidden`, `ErrServiceUnavailable`) distinguishing 401/403/503. No dependency beyond the standard library. |
| `auth/zitadel` | Zitadel adapter (`Authenticator`) wrapping `zitadel-go/v3`'s `authorization.Authorizer[*oauth.IntrospectionContext]` — that generic type never appears outside this package. Prefers the OIDC-standard `preferred_username` claim over the legacy `Username` field. |
| `authz` | The `Checker` port: `IsAllowed(ctx, Principal, Resource, action) (bool, error)`. A non-nil error means the decision could not be made (map it to 503); `false` means a genuine denial (map it to 403). No dependency beyond the standard library. |
| `authz/cerbos` | Cerbos adapter (`Checker`) via `cerbos-sdk-go`'s gRPC client. `Close()` is a documented no-op — `cerbos.GRPCClient` in v0.3.17 exposes no `Close` method, so there is nothing to release. |
| `pgxtx` | pgx transaction plumbing: `PgxTxManager.WithTx`, `DBFromContext`, `TxFromContext`, the `DBTX` interface. Named `pgxtx` (not `repository`) because it holds transaction plumbing, not repositories. Lives at the module root, as a sibling of `postgres`, because `WithTx` works against any `*pgxpool.Pool` the consumer built themselves. |
| `audit` | The audit-trail port: `Entry`, the `Auditable` interface (`AuditRepr`/`AuditSnapshot`), the `Recorder` (`Record`, plus functional `Option`s: `WithSubject`, `WithChange`, `WithAggregate`, `WithAffectedResources`, `WithError`, ...), and the `Repository` port (`Create`/`GetByID`/`List`). Reads the actor from `auth.FromContext` and request metadata from `reqctx` — never imports `httpx` or a router (`go list -deps ./audit` carries no `chi`, no `httpx`; see fix 17). `Recorder.Record` takes a `Source` (`SourceHTTP`/`SourceWorker`) as a required argument, not a defaultable option — see fix 20. |
| `audit/postgres` | PostgreSQL-backed `Repository` (`NewRepository(pool)`), ported from go-licencias: a single `List` query using `count(*) OVER()` for pagination (one round trip, not two) and a `Filters.ResourceID` typed `*uuid.UUID` (not `string`). |
| `audit/migrations` | The embedded `001_create_audit_log.sql` migration (byte-identical across go-bluprint, go-crucible, and go-licencias) exposed as an `fs.FS` via `migrations.FS()`, ready to pass to `migrate.Up`. See "Running library migrations alongside application migrations" below. |
| `request` | HTTP request helpers: `JSON` / `JSONWithLimit` (size-limited, unknown-field-rejecting JSON decoding with mapped 400/413 responses) and `Validator` (accumulates per-field validation errors for query params and chi URL params: `UUIDParam`, `IntQuery`, `TimeQuery`, `DateQuery`, `Enum`, `PublicIDParam`, ...). |
| `config` | Primitives for reading and validating environment variables: typed readers with defaults (`String`, `Bool`, `Int`, `Int32`, `Duration`, `StringSlice`), a required-variable reader (`Require`), an `Errors` accumulator so a boot failure reports every problem at once, and semantic validators (`ValidURL`, `IntRange`, `OneOf`, `MinMax`). Deliberately does **not** define application config structs — those stay in the consuming app. |
| `postgres` | `NewPool(ctx, Config, *slog.Logger) (*pgxpool.Pool, error)`: a pgxpool constructor with slow-query tracing (`SlowQueryTracer`), Prometheus pool metrics (`PoolMetricsCollector`), server-side `statement_timeout` / `lock_timeout` / `idle_in_transaction_session_timeout` (configurable, sane defaults), and a hard startup error when `RequireTLS` is set but the DSN disables TLS. |
| `migrate` | Programmatic access to goose v3 migrations owned by the *consuming application* (this package embeds none of its own): `Up`, `Down`, `UpTo`, `UpByOne`, `Status`, all taking an explicit `fs.FS`. Every run acquires a PostgreSQL session-level advisory lock via `goose.WithSessionLocker` so concurrent `migrate up` invocations serialize instead of racing. `Options.TableName` selects a non-default goose version table, so an independently-numbered migration set (e.g. `audit/migrations`) can run against the same database as an application's own migrations without colliding — see "Running library migrations alongside application migrations" below. |

## Running library migrations alongside application migrations

`audit/migrations` ships its own `001_create_audit_log.sql`, numbered independently
of whatever an application's own migrations are numbered. If both sets tracked
applied versions in goose's default `goose_db_version` table, the library's `001`
and the application's `001` would collide — whichever ran second would be seen as
"already applied" and silently skipped, or fail outright.

Run the two sets against the same database with two separate calls to
`migrate.Up`, giving the library set its own version table via
`migrate.Options.TableName`:

```go
import (
	"github.com/kafeiih/vogel/audit/migrations"
	"github.com/kafeiih/vogel/migrate"
)

// Application migrations track in goose's default "goose_db_version" table.
if err := migrate.Up(ctx, dbURL, appMigrationsFS); err != nil {
	return err
}

// Library migrations track in their own table, independently versioned from
// the application's own 001, 002, ... — migrations.DefaultTableName is
// "vogel_db_version".
if err := migrate.Up(ctx, dbURL, migrations.FS(), migrate.Options{
	TableName: migrations.DefaultTableName,
}); err != nil {
	return err
}
```

Both calls acquire `migrate`'s PostgreSQL session-level advisory lock independently
(see fix 10 below), so running them back-to-back at application startup is safe
even across multiple replicas racing to migrate on boot. `migrate.Status` and the
other `migrate` functions accept the same `Options` for querying the library set's
state separately from the application's.

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
11. **`auth`/`authz` drop org_id-based multi-tenancy entirely — it never worked.**
    Both source systems mirrored a `UserContext.OrgID` onto every Cerbos resource
    so derived roles could compare `principal.attr.org_id == resource.attr.org_id`,
    but `ResourceForUser` copied that org_id straight off the same principal, so
    the comparison was always `X == X` — always true. Even with that bug fixed,
    the owner's Zitadel instance has exactly one organization ("intranet"), with
    per-system separation done at the project/application level instead, so
    `urn:zitadel:iam:user:resourceowner:id` returns the same value for every user
    in every system: there was never a second value to compare against. `org_id`
    also appears in zero repository queries and zero migrations in either system.
    `Principal.OrgID`, `extractOrgID`, and the org_id mirroring in
    `ResourceForUser`/`principalAttr` are gone, along with the fail-loud "treat as
    unauthenticated if org_id is missing" branch that existed only to protect that
    dead mechanism. The real separation between systems is OIDC audience
    validation, which the Zitadel SDK already performs.
12. **`auth/zitadel` prefers `preferred_username` over the legacy `Username` field.**
    go-crucible read `authCtx.Username` directly, which is empty for tokens from a
    standards-conformant flow; go-licencias already carried this fix. Ported here
    so the one adapter in this module gets it right.
13. **A PDP/IdP outage now maps to HTTP 503, never 401/403/500.** go-crucible
    returned 500 when the Cerbos check errored; go-licencias deliberately returned
    403 (its DEC-08, "to avoid an oracle"); the base collapsed Zitadel's
    `ServiceUnavailableErr` into 403 too. All three make monitoring blind to the
    difference between "the client did something wrong" and "our infrastructure is
    down", and none of 401/403/500 are meaningfully retryable the way 503 is. Both
    `httpx/middleware.Authenticate` (identity provider) and
    `httpx/middleware.RequirePermission` (policy decision point) now map a
    provider-side error to 503, a genuine denial to 403, and missing/invalid
    credentials to 401 — see the doc comments on both for the full rationale. This
    deliberately supersedes go-licencias' DEC-08.
14. **User-facing auth/authz messages are no longer hardcoded Spanish.** The
    sources returned literals like `"No tenés permisos para realizar esta acción"`
    baked into a library — not something a shared package should own in any one
    language. `httpx/middleware.AuthMessages` (with `DefaultAuthMessages` for
    neutral English defaults and `WithAuthMessages` to override) makes the 401/403/503
    copy configurable; each consuming system sets its own localized copy.
15. **`ZitadelAuthWithRole`, `IsGrantedRole`, and `PrincipalFromUser` were not
    ported.** All three were exported in every source repository with zero
    non-test call sites. `RequirePermission`'s `wildcardResourceID = "*"` behavior
    *was* kept — it is load-bearing, not cosmetic: Cerbos rejects any resource with
    an empty ID before evaluating a policy, which otherwise surfaces as a 500 on
    every collection-level (list/create) route.

16. **`audit_log.request_id` is finally assigned.** In all three source
    repositories, `request_id` was declared in the DTO, mapped from the
    entity, and persisted by the repository — and never assigned anywhere.
    Every audit row in production has `request_id = NULL`, which makes it
    impossible to link an audit entry to its request's log lines, to tell
    that several rows written by one operation were one action, or to follow
    a change into a background job it spawned. `audit.Recorder.Record` now
    reads the request ID from `reqctx` in the same unconditional step where
    it reads the acting principal — not behind a separately-skippable
    option — so a future call site cannot populate the actor while
    "forgetting" the request ID the way every call site so far always has.
    Covered by `TestRecord_RequestIDInContext_IsPersistedOnEntry` and
    `TestRecord_NoRequestIDInContext_LeavesRequestIDNilWithoutError` in
    `audit/recorder_test.go`, and round-tripped against a real database in
    `audit/postgres/repository_integration_test.go`.
17. **`audit` no longer imports the HTTP transport layer.** The source
    `application/audit/recorder.go` called
    `middleware.UserFromContext`/`middleware.RequestInfoFromContext` —
    an application-layer port importing `interfaces/http/middleware`, the
    wrong direction, and the reason this package could not ship in slice 3.
    `audit.Recorder` now reads the actor via `vogel/auth.FromContext` and
    request metadata via `vogel/reqctx`, both dependency-light
    application-facing packages. `go list -deps ./audit` carries no `chi`
    and no `httpx`. (It does still carry `net/http`, transitively via
    `vogel/auth`'s `Authenticator` interface, which is declared in the same
    file/package as `Principal` — see the "Contradicts this brief" note in
    this slice's report for why that one part of the "no net/http"
    requirement cannot be satisfied without splitting `auth` itself, which
    was out of this slice's authorized scope.)
18. **Request-scoped metadata has one owner: `reqctx`.** `logger` used to own
    its own request-ID context key; `httpx/middleware.RequestInfoMiddleware`
    separately owned an unexported key for IP/User-Agent, with an exported
    `RequestInfoContextKey()` escape hatch just so tests could inject values.
    Both are now `reqctx.WithRequestID`/`RequestIDFromContext` and
    `reqctx.WithRequestInfo`/`RequestInfoFromContext`. `logger.Logger.WithContext`
    reads the request ID from `reqctx` instead of a key it owns itself, so the
    ID that reaches a log line and the one that reaches an `audit_log` row are
    provably the same value. `httpx/middleware.RequestContext` replaces the old
    `LoggerRequestID` + `RequestInfoMiddleware` pair with one middleware that
    writes all three values into `reqctx`; chi's own `middleware.RequestID`
    remains the upstream ID source. The exported `RequestInfoContextKey()` is
    gone — tests use `reqctx.WithRequestInfo` directly.
19. **library-owned migrations get their own goose version table.**
    `audit/migrations` starts its own numbering at `001`, exactly like every
    consuming application's own migrations do, so a shared
    `goose_db_version` table would see the two `001`s as the same version.
    `migrate.Options` gained `TableName` (a caller-supplied option, not a
    constant hardcoded into `migrate`, since that package owns no migrations
    of its own and has no opinion on any one consumer's naming) so the
    library's migrations track in their own table (`vogel_db_version` by
    convention — see `audit/migrations.DefaultTableName`). Verified against a
    real database, with both sets reusing version `1` and both fully
    applying, in `TestUp_IndependentTableNames_DoNotCollide`
    (`migrate/runner_integration_test.go`).
20. **A background job's audit entries state their origin explicitly.**
    Every source repository's `Recorder.Record` defaulted `Source` to
    `"http"` inside the struct literal, so a worker or scheduled job that
    called it — none ever did, precisely because there was no correct value
    to default to — would have produced an HTTP-shaped row for work that
    never touched a request. `Recorder.Record` now takes `Source`
    (`SourceHTTP` or `SourceWorker`) as a required positional argument, not
    an `Option`, so every call site states its origin instead of inheriting
    one. A `SourceWorker` entry legitimately has no request ID unless its
    context was itself derived from the request that spawned the job — see
    `TestRecord_SourceHTTP_And_SourceWorker` and
    `TestRecord_WorkerOrigin_RequestIDStillPropagatedWhenPresent`.

`WithAffectedResources` is the intended way to audit a bulk/batch operation: one
entry on the primary resource naming every row it touched, rather than looping
and calling `Record` once per affected row inside the same transaction (as
go-licencias' `derecho_cobrar/commands.go` does today) — the latter multiplies
INSERTs and extends lock hold time in proportion to batch size for no benefit,
since `AffectedResources` is itself a queryable JSON column. See the doc comment
on `WithAffectedResources` and `TestWithAffectedResources_SetsListOnSingleEntry`.

## Not here yet

This is a fourth slice. Deliberately excluded, pending decisions:

- **The `/v1` URL prefix / `router.go`** — blocked on convergence decisions between the
  two systems about routing conventions.
- **`worker`, `cmd/`, migrations (the SQL files themselves, beyond `audit`'s own),
  Dockerfile, docker-compose, swagger** — out of scope for a shared library slice;
  these are per-application concerns, not shareable ports/adapters. `migrate/create.go`
  (a migration-file scaffolder) was likewise left out as a CLI/template concern, not
  library API. `worker` in particular is pending: `audit.Source.SourceWorker` and
  `Recorder.Record`'s explicit-origin API exist to make a worker's audit trail
  correct once a `worker` package is designed, but no worker abstraction ships yet.
- **`audit`'s Queries/DTO/HTTP handler layer** — the source repositories'
  `application/audit/{dto,queries}.go` and `interfaces/http/handler/audit_handler.go`
  were read for context but not ported: they are presentation-layer concerns
  (response shaping, query-string parsing, Swagger annotations) specific to each
  consuming application's own HTTP layer, the same reason `vogel` has never ported
  a handler for `auth` or `storage` either. `audit.Repository.List`/`GetByID` and
  `audit.Filters` are the port a consuming application's own query layer is built on.
- **`null_helpers.go`** (`nullInt`/`nullInt64`) — left behind deliberately. It returns
  `nil` when `n == 0`, conflating "unset" with a legitimate zero value. It was also
  unused in all three source repositories.
