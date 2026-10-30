Objective: make every effect run visible and safe to retry. Record one row per attempt with its outcome, give handlers a stable idempotency key, and hand them the triggering actor and correlation ids. Target v0.18.x (additive). Feature 3 of 4. Depends on `workflow-trace-metadata.md` (structured actor, request/trace ids). `workflow-tokens.md` later adds `TokenID` to `Args`/`Run` (its S12).
Branch: feat/action-effect-attempts from master after v0.18.0. Delivery: ask-on-risk, one PR. Test runner: `go test -race ./action/... ./document/...`, `go test -tags integration ./action/... ./document/...`, `cd examples/boilerplate && go test ./...`, `cmd/create-vogel-app/check-generated.sh`.

## Specs

Source audits: `simple/docs/reverse-engineering/modules/01-case-trace.md` §5.1 TR-3, TR-20, TR-24; §5.2 A3; §6.4 (effect-attempt log, at-least-once + dedupe). Today effects are enqueued in the transition's tx with uniqueness on `(case, seq, effect, part)` (`vogel/action/action.go:117-137,200-219`), `Worker.Work` runs the handler (`vogel/action/worker.go:29-50`), and nothing records attempts or outcomes. River's job rows are operational and are pruned by retention (Inferred).

- **S1 Idempotency key.** `func (r Run) IdempotencyKey() string` returns `<case>/<seq>/<effect>` for `Part == 0` and `<case>/<seq>/<effect>/<part>` otherwise. For part 0 this equals docflow's current source key (`vogel/document/docflow/docflow.go:62-63`). docflow switches to `r.IdempotencyKey()` with byte-identical keys, so no existing `document_record.source_key` changes. Handlers calling partners pass it outward, e.g. as an `Idempotency-Key` header.
- **S2 Attempt log.** New package `action/migrations` (`migrations.FS()`, version table `action_db_version`, like `workflow/migrations`) with `001_create_action_attempt.sql`. Opt-in `WorkerOption` `WithAttemptLog(db pgxtx.DBTX)`, normally the pool. After the handler returns, `Worker.Work` inserts one row in its own statement on that db, never inside the job's work, with:
  - `case_id`, `seq`, `effect`, `part`, `attempt`, River `job_id`;
  - `started_at`, `finished_at`;
  - `outcome`: `succeeded` | `retry` | `cancelled` | `snoozed` | `discarded` | `unknown_effect`;
  - `error_class`: the Go type plus the matched sentinel. Never the error message, the same rule as `otelpgx` (L15 of `odd/tasks/otel-tracing.md`), because messages can carry personal data;
  - `request_id`, `trace_id`, `actor_kind`, `actor_id`, `result_ref`, `result_digest`.
  
  `discarded` is written when the attempt was River's last and the handler failed. A failed insert never fails or retries the job: it is logged with context and counted by a new metric `vogel_action_attempt_log_errors_total`. Without the option, nothing is written (backward compatible).
- **S3 Handler results.** `action.RecordResult(ctx, Result{Ref, Digest string})` lets a handler attach a reference (e.g. a `document_record` id or a partner's receipt id) and a digest of the response to its attempt row. `docflow.Generate` records the document id and sha256.
- **S4 Read API.** `action.AttemptRepository` with `ListByCase(ctx, db, caseID) ([]Attempt, error)` and `ListByEvent(ctx, db, caseID, seq)`, plus a Postgres implementation in `action/postgres`. Consumers merge it into their timeline read model (TR-22). Rows are append-only, enforced by the same trigger pattern as `vogel/audit/migrations/002_audit_log_append_only.sql`.
- **S5 Actor and correlation for handlers.** `Args`/`Run` gain:
  - `Actor` (the structured actor of the triggering event, from `workflow-trace-metadata.md` S2). `Run.ActorID` stays, mirrored and deprecated.
  - `Run.SystemActor() workflow.Actor`, which returns `{Kind: ActorSystem, ID: "vogel_action:" + Effect, OnBehalfOf: Actor.ID}`. Handlers use it when they write back into a case, so engine work is never attributed to a session user (TR-8, TR-24).
  
  The request id already travels via job metadata (`vogel/worker/requestid.go:18-25`) and the trace via `vogel/otel/otelriver`. The attempt row reads both from ctx.
- **S6 Docs.** README rows for `action` and `action/migrations`, a CHANGELOG `Agregado` entry (Spanish), and a MIGRATION_GUIDE note that `action/migrations` is a new set with its own version table. The boilerplate adds the set to `migrationSets()` (engorro has the same list, `engorro/cmd/api/main.go:170-178`) and passes `WithAttemptLog(pool)` in the worker binary.

### Migration draft `action/migrations/001_create_action_attempt.sql`

```sql
-- +goose Up
CREATE TABLE action_attempt (
    id             UUID PRIMARY KEY,
    case_id        UUID   NOT NULL,           -- no FK: action is usable without workflow's tables, like document_record
    seq            BIGINT NOT NULL,
    effect         TEXT   NOT NULL,
    part           INT    NOT NULL DEFAULT 0,
    attempt        INT    NOT NULL CHECK (attempt >= 1),
    job_id         BIGINT NOT NULL,
    started_at     TIMESTAMPTZ NOT NULL,
    finished_at    TIMESTAMPTZ NOT NULL,
    outcome        TEXT NOT NULL CHECK (outcome IN ('succeeded','retry','cancelled','snoozed','discarded','unknown_effect')),
    error_class    TEXT NOT NULL DEFAULT '',
    request_id     TEXT NOT NULL DEFAULT '',
    trace_id       TEXT NOT NULL DEFAULT '',
    actor_kind     TEXT NOT NULL DEFAULT '',
    actor_id       TEXT NOT NULL DEFAULT '',
    result_ref     TEXT NOT NULL DEFAULT '',
    result_digest  TEXT NOT NULL DEFAULT '',
    UNIQUE (job_id, attempt)
);
CREATE INDEX idx_action_attempt_case_seq ON action_attempt (case_id, seq);
CREATE INDEX idx_action_attempt_outcome ON action_attempt (outcome, finished_at DESC) WHERE outcome IN ('retry','discarded','unknown_effect');
-- + append-only function and triggers (copy of the audit_log pattern)

-- +goose Down
DROP TABLE action_attempt;
```

`UNIQUE (job_id, attempt)` makes a duplicated write after a worker crash a no-op (`ON CONFLICT DO NOTHING`).

### RED tests

| Spec | Tests |
|---|---|
| S1 | `TestRun_IdempotencyKey_Part0MatchesDocflowSourceKey`, `TestRun_IdempotencyKey_IncludesPart`, `TestDocflowGenerate_SourceKeyUnchanged` |
| S2 | `TestWorker_RecordsSucceededAttempt`, `TestWorker_RecordsRetryWithErrorClassNotMessage`, `TestWorker_RecordsCancelledOnJobCancel`, `TestWorker_RecordsDiscardedOnLastAttempt`, `TestWorker_RecordsUnknownEffect`, `TestWorker_AttemptLogFailureDoesNotFailJob`, `TestWorker_WithoutAttemptLogWritesNothing`, `TestAttemptLog_Integration_OneRowPerAttemptAndDuplicateIsNoop` (integration) |
| S3 | `TestRecordResult_StoresRefAndDigest`, `TestDocflowGenerate_RecordsDocumentIDAndSHA` |
| S4 | `TestAttemptRepository_ListByCaseOrdersBySeqThenAttempt` (integration), `TestActionAttempt_UpdateIsRejected` (integration) |
| S5 | `TestDispatch_CopiesStructuredActor`, `TestRun_SystemActorOnBehalfOfTriggeringActor`, `TestWorker_AttemptCarriesRequestIDFromJobMetadata` |

### Breaking changes

None. Every change is additive or opt-in. `Run.ActorID` is deprecated, not removed.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S1 | inline | done | RED: `IdempotencyKey undefined`; GREEN `go test -race ./action/... ./document/...` |
| T2 | S2 S4 (migration, worker option, repository) | inline (Oscar) | pending | — |
| T3 | S3 S5 | inline (Oscar) | pending | — |
| T4 | S6 | inline (Oscar) | pending | — |

Risk: item 1 (new persisted table) and item 5 (worker runtime path). T2 gets its own commit.

## Log

- L1 2026-10-28 Oscar: "vogel es mío, vamos con la opción A, pero si es necesario corregir algo de vogel armame un strachpad con todo lo necesario y lo levanto en un worktree"
- L2 2026-10-28 Derived from `simple/docs/reverse-engineering/modules/01-case-trace.md` §5.2 A3 and §6.4. The scheduler and expiry parts of A3 live in `workflow-case-operations.md`.
- L3 2026-10-30 Branch was cut before v0.18.0 (no `workflow.Actor`); recreated from `origin/master` (3ae0edc) since it had no commits. Integration tests cannot run locally (no Docker daemon); they compile with `-tags integration` and run in CI.
