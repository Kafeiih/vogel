Objective: give every `workflow` event the metadata an audit needs (schema version, structured actor, tenant, request/trace ids, kind-specific detail), make `workflow_event` append-only in the database, expose sequence numbers to callers, and record step submissions and rejected moves. Target v0.18.0 (breaking, 0.x minor bump). Feature 1 of 4; it must land before `workflow-tokens.md`.
Branch: feat/workflow-trace-metadata from master (v0.17.1-7-g2f8dfd7). Delivery: ask-on-risk, stacked-to-main slices (PR-A T1-T3, PR-B T4-T6, PR-C T7-T8). Test runner: `go test -race ./...`, `go test -tags integration ./workflow/... ./action/...`, `cd examples/boilerplate && go test ./...`, `cmd/create-vogel-app/check-generated.sh`.

## Specs

Source audits (in the SIMPLE repo): `docs/reverse-engineering/modules/01-case-trace.md` §5.1 (TR-1, TR-8, TR-9, TR-11, TR-13, TR-16, TR-19, TR-27), §5.2 (gaps A1, A5, A6, A9, A12), §6.1, §6.2, §6.5, §6.7; `modules/02-case-data.md` §5.3 G5 and §6.3 (as-of via `seq`). Current code is cited as `vogel/path:line` against the local repo at v0.17.1-7-g2f8dfd7. Commits after v0.17.1 only bump dependencies (`git diff --stat v0.17.1..HEAD` touches only go.mod/go.sum/Dockerfile), so the v0.17.1 line numbers still hold.

- **S1 Event schema version.** `Event.SchemaVersion int`, stored as `workflow_event.schema_version SMALLINT NOT NULL DEFAULT 1`. Rows written before this feature read as 1. The engine writes `CurrentEventSchema` (2 after this feature). `ListEvents` fails with a new `ErrUnsupportedEventSchema` for a version greater than the binary knows, instead of silently dropping fields. A read-side upcaster `upcastEvent(Event) Event` fills defaults for older versions, and rows are never rewritten. Today the Event struct has no version (`vogel/workflow/workflow.go:674-700`).
- **S2 Structured actor.** Add `type ActorKind string` with `ActorHuman`, `ActorOperator`, `ActorSystem`, `ActorAPIClient`, `ActorAnonymous` and `ActorUnspecified` (used for legacy rows). Add `type Actor struct { Kind ActorKind; ID, ActingAs, OnBehalfOf, Via string }`.
  - `ActingAs` is the authority used, e.g. `position@unit` and whether the actor is holder, subrogante or delegate. The caller resolves it at event time, because `ProfileResolver` answers live and can't prove past authority.
  - `OpenInput` and `MoveInput` gain `Actor Actor`. `ActorID` stays as a deprecated alias: an empty `Actor.ID` means `Actor{Kind: ActorHuman, ID: ActorID}`. If both are set and differ, the call fails with `ErrInvalidActor`. An invalid kind, or `ActorSystem` with an empty ID, also fails with `ErrInvalidActor`.
  - `Event.Actor Actor` replaces the bare `ActorID` in reads (`Event.ActorID` is kept, mirrored from `Actor.ID`, deprecated).
  - New input structs `ClaimInput{CaseID, Actor}` and `ReleaseInput{CaseID, Actor, Reason}` with `Engine.ClaimWith` / `Engine.ReleaseWith`. `Claim`/`Release` (`vogel/workflow/engine.go:748-797`) become thin wrappers. This lets `workflow-tokens.md` add `TokenID` and `workflow-case-operations.md` add `ExpectedRevision` without breaking signatures again.
- **S3 Tenant.** `Case.Tenant string` and `OpenInput.Tenant`. `Event.Tenant` is copied from the case so exports and row-level security never need a join. `InboxFilter.Tenant` filters, and empty means any, which keeps existing behavior. The case uniqueness becomes `(tenant, domain, external_id)`. Today it is `UNIQUE (domain, external_id)` (`vogel/workflow/migrations/001_create_workflow.sql:15`). `Repository.GetByExternalID(ctx, db, tenant, domain, externalID)` gains the tenant parameter (breaking). The engine doesn't call it, so only the repository and its fakes change.
- **S4 Correlation ids.** `Event.RequestID` from `reqctx.RequestIDFromContext(ctx)` (`vogel/reqctx/reqctx.go:59`). `Event.TraceID` / `Event.SpanID` from a new `Config.TraceIDs func(ctx) (traceID, spanID string)`, the same port shape as `postgres.Config.TraceIDs` (`vogel/postgres/postgres.go:155-162`). A nil port leaves them empty. `workflow` must not import OpenTelemetry (D1 of `odd/tasks/otel-tracing.md`): `otel.SampledTraceIDs` plugs into the port.
- **S5 Kind-specific detail.** `Event.Detail json.RawMessage`, stored as `JSONB NOT NULL DEFAULT '{}'`, with one exported Go type per kind that uses it. Under this feature:
  - `AssignmentDetail{From, To, Reason}`, written by Claim (`From:""`) and Release (`To:""`). Today assigned/unassigned events carry only the acting user (`vogel/workflow/engine.go:768,792`).
  - `StepDetail` (S7) and `FailureDetail` (S8).

  The detail never holds domain payload values. It holds references and digests only, keeping the "zero domain data" rule (`vogel/workflow/workflow.go:5-12`).
- **S6 Sequence exposed to callers.**
  - `workflow_case.last_seq BIGINT NOT NULL DEFAULT 0` is backfilled to `MAX(seq)` and exposed as `Case.LastSeq`. `nextSeq` uses it instead of reading every event per append (`vogel/workflow/engine.go:877-897` lists all events on each append today). Every Open/Move/Claim/Release writes it through the existing revision CAS (`vogel/workflow/postgres/repository.go:117-147`).
  - New `Engine.MoveDetailed(ctx, db, MoveInput) (*MoveResult, error)` and `Engine.OpenDetailed`, with `MoveResult{Case *Case; Events []Event}`. `Move`/`Open` (`vogel/workflow/engine.go:283-336,545-658`) wrap them unchanged.
  - Consumers stamp the triggering `seq` on their own rows. Effects already receive `Run.Seq` (`vogel/action/action.go:75-93`).
- **S7 Step submission event.** `MoveInput.Step *StepRecord{StepID string; FormVersion int; PayloadDigest string}`. When it is set, Move appends `EventStepSubmitted` (kind `step_submitted`, detail = the record) immediately before the human `moved` event, in the same tx and seq block. The engine never sees the payload, only its digest.
- **S8 Rejected-move recording.** New `Engine.RecordFailure(ctx, db, FailureInput{CaseID, Actor, Action string, Kind FailureKind, Detail json.RawMessage})`.
  - `FailureKind` is one of `no_route`, `guard_rejected`, `conflict`, `validation_failed`, `comment_rejected`, `return_not_visited`, `transition_not_found`.
  - It appends `EventNoRoute` (kind `no_route`) or `EventFailed` (kind `failed`) without changing state or `revision`. It serializes on the case row with `UPDATE workflow_case SET last_seq = last_seq + 1 WHERE id = $1 RETURNING last_seq`.
  - The caller runs it in its own tx after rolling back the failed one. Helper `FailureKindOf(err error) (FailureKind, bool)` maps the sentinels in `vogel/workflow/workflow.go:703-728`.
- **S9 Database-enforced append-only log.**
  - A new migration installs `vogel_workflow_event_reject_mutation()` plus BEFORE UPDATE OR DELETE (row) and BEFORE TRUNCATE (statement) triggers on `workflow_event`. They raise SQLSTATE 23001 (`restrict_violation`) with the message prefix `workflow_event is append-only`. This copies the audit pattern (`vogel/audit/migrations/002_audit_log_append_only.sql`).
  - The `workflow_event.case_id` FK changes from `ON DELETE CASCADE` (`vogel/workflow/migrations/001_create_workflow.sql:27`) to `ON DELETE RESTRICT`.
  - `workflow/migrations/migrations.go` documents, as audit does, that tests must use a fresh database rather than DELETE/TRUNCATE. A grep finds no workflow test that deletes rows today.
  - Down drops the triggers and restores CASCADE.
- **S10 Participation query.** Index `idx_workflow_event_actor (actor_id, case_id) WHERE actor_id <> ''`. Add `Engine.Participated(ctx, db, ParticipationFilter{Tenant, ActorID, Limit, Offset}) ([]Case, error)` (distinct cases where the actor appears in any event, newest activity first) and the paging rules of `InboxFilter.Page` (`vogel/workflow/workflow.go:804-828`). This closes TR-11 at the engine level.
- **S11 Per-case hash chain (P1, its own slice).**
  - `workflow_event.prev_digest BYTEA NULL` and `digest BYTEA NULL`. `digest = SHA-256(prev_digest || canonical(event))`, where `canonical` is a length-prefixed encoding of every stored column, `schema_version` included.
  - It is computed in `appendEvent` (`vogel/workflow/engine.go:851-873`). Appends are already serialized per case by the row CAS.
  - The first event appended after the migration starts the chain (`prev_digest` NULL). Older rows stay unchained, and this is documented.
  - `Engine.VerifyChain(ctx, db, caseID) error` returns `ErrChainBroken` with the first bad seq.
- **S12 Docs and template.**
  - README rows for `workflow`, `workflow/postgres` and `workflow/migrations`, a CHANGELOG `[Unreleased]` with `Cambiado (rompe)` notes, and a MIGRATION_GUIDE section, all in Spanish, matching the existing files.
  - `examples/boilerplate` compiles against the new API, passes `Actor{Kind: ActorHuman, ID: principal}` and maps `RecordFailure` on rejected submits. `check-generated.sh` passes.

### Migration drafts

`workflow/migrations/004_workflow_event_metadata.sql`:

```sql
-- +goose Up
ALTER TABLE workflow_case
    ADD COLUMN tenant   TEXT   NOT NULL DEFAULT '',
    ADD COLUMN last_seq BIGINT NOT NULL DEFAULT 0;
UPDATE workflow_case c
   SET last_seq = COALESCE((SELECT MAX(e.seq) FROM workflow_event e WHERE e.case_id = c.id), 0);
-- Constraint name is Postgres' default for UNIQUE (domain, external_id) in 001 (Inferred: verify with \d workflow_case).
ALTER TABLE workflow_case DROP CONSTRAINT workflow_case_domain_external_id_key;
CREATE UNIQUE INDEX uq_workflow_case_tenant_domain_external_id
    ON workflow_case (tenant, domain, external_id);

ALTER TABLE workflow_event
    ADD COLUMN schema_version SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN tenant         TEXT     NOT NULL DEFAULT '',
    ADD COLUMN actor_kind     TEXT     NOT NULL DEFAULT 'unspecified',
    ADD COLUMN acting_as      TEXT     NOT NULL DEFAULT '',
    ADD COLUMN on_behalf_of   TEXT     NOT NULL DEFAULT '',
    ADD COLUMN via_client     TEXT     NOT NULL DEFAULT '',
    ADD COLUMN request_id     TEXT     NOT NULL DEFAULT '',
    ADD COLUMN trace_id       TEXT     NOT NULL DEFAULT '',
    ADD COLUMN span_id        TEXT     NOT NULL DEFAULT '',
    ADD COLUMN detail         JSONB    NOT NULL DEFAULT '{}';
CREATE INDEX idx_workflow_event_actor ON workflow_event (actor_id, case_id) WHERE actor_id <> '';

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_event_actor;
ALTER TABLE workflow_event
    DROP COLUMN detail, DROP COLUMN span_id, DROP COLUMN trace_id, DROP COLUMN request_id,
    DROP COLUMN via_client, DROP COLUMN on_behalf_of, DROP COLUMN acting_as,
    DROP COLUMN actor_kind, DROP COLUMN tenant, DROP COLUMN schema_version;
DROP INDEX IF EXISTS uq_workflow_case_tenant_domain_external_id;
ALTER TABLE workflow_case ADD CONSTRAINT workflow_case_domain_external_id_key UNIQUE (domain, external_id);
ALTER TABLE workflow_case DROP COLUMN last_seq, DROP COLUMN tenant;
```

`workflow/migrations/005_workflow_event_append_only.sql`: the S9 function and triggers (copy the structure of `vogel/audit/migrations/002_audit_log_append_only.sql`), plus:

```sql
ALTER TABLE workflow_event DROP CONSTRAINT workflow_event_case_id_fkey;
ALTER TABLE workflow_event ADD CONSTRAINT workflow_event_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES workflow_case(id) ON DELETE RESTRICT;
```

The S11 columns go in `006_workflow_event_digest.sql` in its own slice. If `workflow-tokens.md` lands first, renumber.

Rollout (same caveat as 003 in README `workflow/migrations`, `vogel/README.md:122`): an old binary appends events without advancing `last_seq`. A new binary then computes a seq that collides with `UNIQUE (case_id, seq)` and fails loudly instead of corrupting history. Apply 004–005, then replace every binary together.

### RED tests (write first, observe failing)

| Spec | Test |
|---|---|
| S1 | `TestListEvents_LegacyRowReadsAsSchemaVersion1` (integration), `TestListEvents_UnknownSchemaVersionFails`, `TestAppendEvent_WritesCurrentSchemaVersion` |
| S2 | `TestOpen_ActorIDAliasBecomesHumanActor`, `TestMove_ConflictingActorAndActorIDIsInvalid`, `TestMove_SystemActorNeedsID`, `TestEvent_PersistsActingAsAndOnBehalfOf` (integration) |
| S3 | `TestOpen_SameExternalIDInTwoTenantsIsAllowed` (integration), `TestInbox_FiltersByTenant`, `TestEvent_CopiesCaseTenant` |
| S4 | `TestMove_StampsRequestIDFromContext`, `TestMove_StampsTraceIDsFromPort`, `TestMove_NilTraceIDsPortLeavesEmpty` |
| S5 | `TestClaim_RecordsAssignmentFromTo`, `TestRelease_RecordsPreviousAssigneeAndReason` |
| S6 | `TestMoveDetailed_ReturnsAppendedEventsInSeqOrder`, `TestMove_AdvancesLastSeq`, `TestNextSeq_DoesNotListEvents` (call-counting repo, as `listEventsCountingRepository` in `vogel/workflow/engine_test.go:1423`), `TestMigration004_BackfillsLastSeq` (integration) |
| S7 | `TestMove_WithStepAppendsStepSubmittedBeforeMoved`, `TestMove_StepDetailHoldsDigestOnly` |
| S8 | `TestRecordFailure_AppendsWithoutChangingStateOrRevision`, `TestRecordFailure_NoRouteKind`, `TestFailureKindOf_MapsEverySentinel` |
| S9 | `TestWorkflowEvent_UpdateIsRejected`, `TestWorkflowEvent_DeleteIsRejected`, `TestWorkflowEvent_TruncateIsRejected`, `TestWorkflowCase_DeleteWithEventsIsRestricted` (all integration, mirror `vogel/audit/postgres/append_only_integration_test.go`) |
| S10 | `TestParticipated_ReturnsCasesWhereActorActedAfterReassign`, `TestParticipated_RejectsOffsetOutOfRange` |
| S11 | `TestAppendEvent_ChainsDigest`, `TestVerifyChain_DetectsAlteredRow` (integration, triggers disabled inside the test tx), `TestVerifyChain_LegacyRowsAreUnchained` |

Keep every existing test green: the old-signature wrappers (`Move`, `Open`, `Claim`, `Release`) must pass the current suite unmodified (56 tests in `vogel/workflow/engine_test.go`).

### Breaking changes (CHANGELOG "Cambiado (rompe)")

- `Repository` interface: `GetByExternalID` gains `tenant`. New methods: `RecordFailure` support (`AppendEventLocked` or equivalent) and `ListParticipated`. Custom repositories and test fakes must implement them.
- Uniqueness of a case is now `(tenant, domain, external_id)`.
- `workflow_event` rejects UPDATE/DELETE/TRUNCATE, and deleting a case with events is restricted.
- Migrations 004 and 005 are mandatory before deploying, and all binaries must be replaced together.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S1 S5 S6 | inline | done (integration pending) | commit pending; RED 10/10 unit observed (compile, then behavior), GREEN `go test -race ./...`; 6 integration tests vet-clean, not run (no Docker access) |
| T2 | S2 S3 | inline (Oscar) | pending | — |
| T3 | S4 | inline (Oscar) | pending | — |
| T4 | S7 S8 | inline (Oscar) | pending | — |
| T5 | S9 | inline (Oscar) | pending | — |
| T6 | S10 | inline (Oscar) | pending | — |
| T7 | S11 (P1) | inline (Oscar) | pending | — |
| T8 | S12 | inline (Oscar) | pending | — |

Each task carries its README/CHANGELOG lines and boilerplate compile fixes in the same commit. Risk: item 1 (persisted format, migrations 004–006) and item 3 (public API) → independent verifier per slice.

## Log

- L1 2026-10-28 Oscar: "vogel es mío, vamos con la opción A, pero si es necesario corregir algo de vogel armame un strachpad con todo lo necesario y lo levanto en un worktree"
- L2 2026-10-28 Derived from the SIMPLE rewrite audits: `simple/docs/reverse-engineering/modules/01-case-trace.md` §5–§6 (TR-1, TR-8, TR-9, TR-11, TR-13, TR-16, TR-19, TR-27; gaps A1, A5, A6, A9, A12) and `modules/02-case-data.md` §5.3 G5 / §6.3. Engorro-side work stays in engorro (see the handoff README follow-ups).
- L3 2026-10-28 Ordering decision proposed by the handoff: this feature lands before `workflow-tokens.md` because it introduces `schema_version`, the input structs and `last_seq` that tokens reuse, and because a nullable `token_id` column can be added later without UPDATE backfills, so the S9 triggers never get in its way.
- L4 2026-10-28 T1 (S1 S5 S6) inline. Decisions: `Repository.AppendEvent` now stores the engine-assigned `Seq` (no SQL `MAX+1`), so a taken seq fails on UNIQUE as the rollout note requires; Open persists `LastSeq: 1` through `Create`; Move/Claim/Release advance `LastSeq` before the revision CAS. The schema check and `upcastEvent` live in the engine read path (`listEvents`, used by `History` and the return-visited check) because `workflow/postgres` cannot call an unexported upcaster; `TestListEvents_UnknownSchemaVersionFails` therefore goes through `History`. Version 0 (a repository that does not store it) reads as 1. `OpenDetailed` returns `*MoveResult` too. Migration 004 holds only the T1 columns (`last_seq`, `schema_version`, `detail`); T2/T3 extend it in this same unreleased slice.
- L5 2026-10-28 T1 deviations from the spec text: `TestRelease_RecordsPreviousAssigneeAndReason` is split; T1 ships `TestRelease_RecordsPreviousAssignee` (From/To) and T2 adds the Reason half with `ReleaseInput`. Existing tests changed because S6 removes the per-append history read: `TestNextSeqFromEvents` deleted (helper gone), `TestMove_NonReturnTransition_HistoryReadCount` 1→0, `TestMove_ReturnTransition_HistoryReadCount` 2→1, and the two `AppendEvent` integration tests now set `Seq`. CHANGELOG uses the file's existing heading `### Cambios que rompen`, not `Cambiado (rompe)`.
- L6 2026-10-28 Blocker for evidence: Docker socket is `root:docker 660` and the user is not in group `docker`, so no integration test ran (`TestListEvents_LegacyRowReadsAsSchemaVersion1`, `TestMigration004_BackfillsLastSeq`, round-trip, taken-seq, LastSeq persistence, engine end-to-end). They compile under `-tags integration`. Run `go test -tags integration ./workflow/... ./action/...` where Docker is reachable before closing PR-A.
