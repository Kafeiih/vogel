Objective: add parallel branches to `vogel/workflow` with a token/work-item model (BPMN token semantics): AND and inclusive (conditional) splits, exactly-once joins under the existing per-case lock, nested splits, and the token id as the task-instance id for claim, submit, inbox, deadlines, returns, cancellation, events and effects. Target v0.19.0 (breaking). Feature 2 of 4. Depends on `workflow-trace-metadata.md` (`schema_version`, `Event.Detail`, `last_seq`, input structs).
Branch: feat/workflow-tokens from master after v0.18.0. Delivery: ask-on-risk, stacked-to-main slices (PR-A T1-T2 model + validation, PR-B T3-T5 engine, PR-C T6-T8 inbox/ops/effects, PR-D T9-T10 migration + docs). Test runner: `go test -race ./...`, `go test -tags integration ./workflow/... ./action/...`, `cd examples/boilerplate && go test ./...`, `cmd/create-vogel-app/check-generated.sh`.

## Specs

Source audits: `simple/docs/reverse-engineering/modules/01-case-trace.md` §5.1 TR-5, TR-21, TR-23, §5.2 M1 and M2, §6.3 (Option A, decided 2026-10-28); `modules/02-case-data.md` §5.1 DV-8, §5.3 G3, §6.3 (branch-scoped values and merge policies). Today a case has exactly one `State` (`vogel/workflow/workflow.go:651-671`), Move moves it (`vogel/workflow/engine.go:545-658`), and the legacy SIMPLE union race (01 §3 D1) is the behavior to never reproduce.

- **S1 Gateway node kinds.** `NodeParallel` (`"parallel"`, AND split: every outgoing transition is taken, none may have a guard), `NodeInclusive` (`"inclusive"`, OR split: every outgoing route whose guard passes is taken; exactly one unguarded default route, declared last, is taken only when no guarded route passes) and `NodeJoin` (`"join"`). A split names its join with the new `Node.Join string`. Like decision nodes (`vogel/workflow/workflow.go:149-160,437-453`), a case never rests on a split or a join. Inclusive is justified by SIMPLE's `paralelo_evaluacion` connection (`simple/docs/reverse-engineering/03-workflow-engine.md` §3).
- **S2 Block-structured validation** in `Definition.Validate` (`vogel/workflow/workflow.go:375-594`):
  - (a) Every split names an existing `NodeJoin`, and every join is named by exactly one split.
  - (b) Every path from each outgoing transition of a split reaches its own join without passing a terminal node and without leaving the block. Blocks nest properly: an inner block lies entirely inside one branch of the outer block.
  - (c) Terminal nodes exist only in the root scope.
  - (d) A join has exactly one outgoing transition, unguarded.
  - (e) A `Return` transition may not cross a block boundary: source and target must be in the same branch of the same block, or both in the root scope.
  - (f) Splits and joins reject `Eligible`, `Deadline` and node `Effects`, like decision nodes. Their transitions may declare effects.
  - (g) `Comment` policies are rejected on routes leaving a split.
  - (h) The decision-only cycle rule extends to gateway-only cycles.

  Each violation is a separate line in the `ErrInvalidDefinition` error, as today.
- **S3 Token and scope model.**
  - `type TokenStatus string`: `active`, `waiting`, `consumed`, `cancelled`.
  - `type Token struct { ID, CaseID, ScopeID uuid.UUID; Node string; Status TokenStatus; AssignedTo string; DeadlineAt *time.Time; EnteredSeq int64; EnteredAt time.Time; EndedSeq int64; EndedAt *time.Time }`.
  - `type ScopeStatus string`: `open`, `joined`, `cancelled`, `closed`.
  - `type Scope struct { ID, CaseID uuid.UUID; ParentID *uuid.UUID; SplitNode, JoinNode string; Expected int; Status ScopeStatus; OpenedSeq, ClosedSeq int64 }`.
  - The root scope's id equals the case id. A case that never split keeps one root token whose id equals the case id, so legacy rows and single-token definitions need no new identifiers.
  - Tokens are mutable state, like `workflow_case`. History stays in `workflow_event`.
- **S4 Split.** A Move whose plan reaches a split, in the same call and tx (extending `planMove`, `vogel/workflow/engine.go:673-699`):
  - consumes the arriving token;
  - opens a `Scope` with `Expected` = the number of tokens emitted;
  - creates one token per taken route and routes each one through decision nodes and nested splits until it rests on a task node or arrives at its join.
  - Events: one `split` event (detail `{scope, split_node, tokens[]}`), then each token's `moved` hops carrying `token_id`/`scope_id`. A branch with no task nodes arrives at the join in the same call.
- **S5 Exactly-once join.**
  - A token arriving at its scope's join becomes `waiting` and appends `waiting`.
  - When `waiting + cancelled == Expected` and at least one token is waiting, the engine consumes every waiting token, sets the scope to `joined`, appends `joined` (detail `{scope, join_node, tokens[]}`), creates one token in the parent scope and continues routing from the join's outgoing transition. This may cascade into an outer join.
  - Every token or scope mutation bumps the case `revision` in the same `Update` CAS (`vogel/workflow/postgres/repository.go:117-147`), so all moves on one case are serialized. Two concurrent last-branch completions produce one success and one `ErrConflict`. The retry sees the other branch already waiting and does not join again.
  - A partial unique index on `joined` events per scope makes a second join impossible even if application logic regresses.
  - Tradeoff: branches of one case can't commit concurrently. That's acceptable for human-paced tasks (Inferred) and gives correctness without new lock types.
- **S6 Inclusive join counting.** The join waits for `Scope.Expected`, which is the number of tokens the split actually emitted. With block structure (S2b) no dead-path elimination is needed.
- **S7 Per-token operations.**
  - `MoveInput.TokenID`, `ClaimInput.TokenID` and `ReleaseInput.TokenID` (input structs from `workflow-trace-metadata.md` S2). A zero value means "the case's only active token". With two or more active tokens the call fails with the new `ErrTokenRequired`, and a token not active in that case fails with `ErrTokenNotFound`.
  - New `Engine.AvailableFor(ctx, db, caseID, tokenID)` and `Engine.EligibilityForToken(ctx, db, caseID, tokenID)`. `Available` and `EligibilityFor` (`vogel/workflow/engine.go:345-389,827-847`) keep their signatures for single-token cases and return `ErrTokenRequired` otherwise.
  - `Engine.Tokens(ctx, db, caseID) ([]Token, error)` lists every token, oldest first.
  - `Case.State`, `Case.AssignedTo` and `Case.DeadlineAt` become projections: the sole active token's node and assignee, or `""`/nil when zero or several tokens are active, and the earliest active-token deadline.
- **S8 Inbox per token (breaking).**
  - `Engine.Inbox` returns `[]WorkItem{Case Case; Token Token}`. `InboxFilter.States`, `AssignedTo`, `Unassigned` and `Overdue` apply to the token.
  - The query reads `workflow_token` joined to `workflow_case`, replacing the case-only query (`vogel/workflow/postgres/repository.go:193-237`), with the same paging rules.
  - New `InboxFilter.Eligibility []Eligibility` lets a consumer filter by `(position, unit)` in SQL. Each token stores its node's resolved eligibility in `position`/`unit` columns. This removes the consumer-side filter that causes E-D1 truncation and E-D2 N+1 in the boilerplate (`vogel/examples/boilerplate/internal/application/process/service.go:80-106`).
- **S9 Returns with tokens.** `occupiedStates` (`vogel/workflow/engine.go:415-441`) is computed from events of the current scope only (`scope_id` = the moving token's scope). S2e guarantees a return never needs another scope. A return target not occupied in the scope fails with `ErrReturnNotVisited`, as today.
- **S10 Cancellation.**
  - `Engine.Cancel(ctx, db, CancelInput{CaseID, Actor, Reason})`: `Reason` is required (`ErrReasonRequired`). It cancels every active and waiting token (one `token_cancelled` event each), sets every open scope to `cancelled`, sets `Case.Status = StatusCancelled` (new constant, `Status.Valid` updated, `vogel/workflow/workflow.go:39-52`) and appends one `cancelled` event.
  - `Engine.CancelToken(ctx, db, CancelTokenInput{CaseID, TokenID, Actor, Reason})` cancels one branch and counts it toward the join (S5), which may fire the join. It fails with the new `ErrLastBranch` when it would leave a scope with no active or waiting token. Use `Cancel` instead, so an open case never ends without a live token (TR-23).
  - Reaching a terminal node can never close sibling branches, because terminals exist only in the root scope (S2c, TR-21).
- **S11 Deadlines per token.** `Node.Deadline` (`vogel/workflow/workflow.go:222-224`) is applied per token on entry (`Token.DeadlineAt`). `InboxFilter.Overdue` is evaluated per token. `Case.DeadlineAt` is the projection from S7.
- **S12 Events and effects reference the token.**
  - `Event.TokenID` and `Event.ScopeID` are stored as nullable `token_id`/`scope_id` columns. NULL means a legacy root event, and reads return the case id via `COALESCE`, so the append-only triggers from `workflow-trace-metadata.md` S9 never need an UPDATE backfill.
  - The engine writes `CurrentEventSchema = 3`.
  - `action.Args`/`action.Run` gain `TokenID` (copied from the event in `plan`, `vogel/action/action.go:222-245`). The job uniqueness `(case, seq, effect, part)` (`vogel/action/action.go:117-137`) is unchanged, because seq is unique per case.
  - New kinds: `split`, `waiting`, `joined`, `token_cancelled`, `cancelled`.
- **S13 Invariants.** These hold after every committed engine call, checked by a test helper `assertInvariants(t, repo, caseID)` and by a randomized walk test:
  - I1: open ⇔ at least one active token.
  - I2: closed or cancelled ⇔ zero active and zero waiting tokens.
  - I3: every waiting token belongs to an open scope at its join node.
  - I4: `Scope.Expected` equals the tokens created in it.
  - I5: no token rests on a decision, split or join node.
  - I6: the `Case.State`/`AssignedTo`/`DeadlineAt` projections match S7.
  - I7: at most one `joined` event per scope.
  - I8: replaying the events of a case reproduces its tokens and scopes (TR-22).
- **S14 Backward compatibility.**
  - Definitions without splits behave exactly as today. The existing 56 engine tests, the definition tests and the effects tests pass, changed only for the `Inbox` return type.
  - Migration 007 materializes one root scope and one root token per existing case:
    - open case → `active` token at `state`, with `assigned_to` and `deadline_at` copied and `entered_seq` = the max seq of the opened/moved event that entered `state`;
    - closed case → `consumed` token and a `closed` root scope.
  - A compatibility shim: an open case with no token rows (written by an old binary after the migration) gets its root token synthesized on first read and persisted under the case CAS. It's marked deprecated and slated for removal in v0.21.0.
  - Rolling deploys between pre- and post-token binaries are unsafe. Deploy all binaries together after the migration, and document this like migration 003.
- **S15 Branch-scope hook for consumers** (for DV-8 in `modules/02-case-data.md`). `MoveResult` (from `workflow-trace-metadata.md` S6) includes `Joined []JoinRecord{Scope, Tokens}`. A new optional `Config.JoinCheck func(ctx, db, JoinRecord) error` runs inside the join tx before the join commits. A non-nil error aborts the Move with `ErrJoinRejected` wrapping it. Consumers use it to enforce variable merge policies (`reject`, `namespace`, `collect`, ...) without the engine reading domain data.
- **S16 Docs and template.** README `workflow` row (Spanish): token model, gateways, invariants, per-token API. CHANGELOG `[Unreleased]` with `Agregado` and `Cambiado (rompe)`. MIGRATION_GUIDE section "De un estado a tokens". `examples/boilerplate` uses `WorkItem` in the inbox, passes `TokenID` on claim and submit, and fixes E-D1/E-D2 with `InboxFilter.Eligibility`. `check-generated.sh` passes.

### Proposed Go API (sketch)

```go
const (
    NodeParallel  NodeKind = "parallel"
    NodeInclusive NodeKind = "inclusive"
    NodeJoin      NodeKind = "join"
)
type Node struct { /* existing fields */ Join string }

const StatusCancelled Status = "cancelled"

const (
    EventSplit          EventKind = "split"
    EventWaiting        EventKind = "waiting"
    EventJoined         EventKind = "joined"
    EventTokenCancelled EventKind = "token_cancelled"
    EventCancelled      EventKind = "cancelled"
)

type WorkItem struct { Case Case; Token Token }
type JoinRecord struct { Scope Scope; Tokens []Token }

var (
    ErrTokenRequired = errors.New("workflow: case has several active tokens; name one")
    ErrTokenNotFound = errors.New("workflow: token is not active in this case")
    ErrLastBranch    = errors.New("workflow: cancelling this token would leave its scope without a live branch")
    ErrJoinRejected  = errors.New("workflow: join rejected by the consumer's join check")
    ErrReasonRequired = errors.New("workflow: a reason is required")
)

func (e *Engine) Tokens(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Token, error)
func (e *Engine) AvailableFor(ctx context.Context, db pgxtx.DBTX, caseID, tokenID uuid.UUID) ([]Transition, error)
func (e *Engine) EligibilityForToken(ctx context.Context, db pgxtx.DBTX, caseID, tokenID uuid.UUID) (Eligibility, error)
func (e *Engine) Inbox(ctx context.Context, db pgxtx.DBTX, f InboxFilter) ([]WorkItem, error) // breaking
func (e *Engine) Cancel(ctx context.Context, db pgxtx.DBTX, in CancelInput) (*Case, error)
func (e *Engine) CancelToken(ctx context.Context, db pgxtx.DBTX, in CancelTokenInput) (*MoveResult, error)

// Repository gains (breaking for custom implementations and fakes):
CreateScope(ctx, db, *Scope) error
UpdateScope(ctx, db, *Scope) error
ListScopes(ctx, db, caseID uuid.UUID) ([]Scope, error)
CreateToken(ctx, db, *Token) error
UpdateToken(ctx, db, *Token) error
ListTokens(ctx, db, caseID uuid.UUID) ([]Token, error)
ListWorkItems(ctx, db, InboxFilter) ([]WorkItem, error) // replaces ListByEligibility
```

Token and scope writes happen only after the case `Update` CAS has succeeded in the same tx. That is the serialization point, so tokens need no revision of their own.

### Migration draft `workflow/migrations/007_workflow_tokens.sql`

Number it after the `workflow-trace-metadata.md` migrations (004–006).

```sql
-- +goose Up
CREATE TABLE workflow_scope (
    id          UUID PRIMARY KEY,
    case_id     UUID NOT NULL REFERENCES workflow_case(id) ON DELETE RESTRICT,
    parent_id   UUID NULL REFERENCES workflow_scope(id) ON DELETE RESTRICT,
    split_node  TEXT NOT NULL DEFAULT '',
    join_node   TEXT NOT NULL DEFAULT '',
    expected    INT  NOT NULL CHECK (expected >= 1),
    status      TEXT NOT NULL CHECK (status IN ('open', 'joined', 'cancelled', 'closed')),
    opened_seq  BIGINT NOT NULL,
    closed_seq  BIGINT NULL,
    CHECK ((parent_id IS NULL) = (id = case_id))
);
CREATE INDEX idx_workflow_scope_case ON workflow_scope (case_id);

CREATE TABLE workflow_token (
    id           UUID PRIMARY KEY,
    case_id      UUID NOT NULL REFERENCES workflow_case(id) ON DELETE RESTRICT,
    scope_id     UUID NOT NULL REFERENCES workflow_scope(id) ON DELETE RESTRICT,
    node         TEXT NOT NULL,
    position     TEXT NOT NULL DEFAULT '',
    unit         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL CHECK (status IN ('active', 'waiting', 'consumed', 'cancelled')),
    assigned_to  TEXT NOT NULL DEFAULT '',
    deadline_at  TIMESTAMPTZ NULL,
    entered_seq  BIGINT NOT NULL,
    entered_at   TIMESTAMPTZ NOT NULL,
    ended_seq    BIGINT NULL,
    ended_at     TIMESTAMPTZ NULL,
    CHECK ((status IN ('active', 'waiting')) = (ended_seq IS NULL))
);
CREATE INDEX idx_workflow_token_case_status ON workflow_token (case_id, status);
CREATE INDEX idx_workflow_token_inbox ON workflow_token (node, position, unit) WHERE status = 'active';
CREATE INDEX idx_workflow_token_assigned ON workflow_token (assigned_to) WHERE status = 'active' AND assigned_to <> '';
CREATE INDEX idx_workflow_token_deadline ON workflow_token (deadline_at) WHERE status = 'active' AND deadline_at IS NOT NULL;

-- Nullable: NULL = legacy root event (token = scope = case id). No UPDATE backfill, so the
-- append-only triggers of 005 stay enabled.
ALTER TABLE workflow_event ADD COLUMN token_id UUID NULL, ADD COLUMN scope_id UUID NULL;
CREATE UNIQUE INDEX uq_workflow_event_joined_once ON workflow_event (scope_id) WHERE kind = 'joined';

INSERT INTO workflow_scope (id, case_id, parent_id, expected, status, opened_seq)
SELECT c.id, c.id, NULL, 1, CASE WHEN c.status = 'open' THEN 'open' ELSE 'closed' END, 1
FROM workflow_case c;

INSERT INTO workflow_token (id, case_id, scope_id, node, status, assigned_to, deadline_at,
                            entered_seq, entered_at, ended_seq, ended_at)
SELECT c.id, c.id, c.id, c.state,
       CASE WHEN c.status = 'open' THEN 'active' ELSE 'consumed' END,
       c.assigned_to, c.deadline_at,
       COALESCE((SELECT MAX(e.seq) FROM workflow_event e
                  WHERE e.case_id = c.id AND e.to_state = c.state AND e.kind IN ('opened', 'moved')), 1),
       c.opened_at,
       CASE WHEN c.status = 'open' THEN NULL ELSE c.last_seq END,
       c.closed_at
FROM workflow_case c;
-- position/unit of existing tokens are filled by the engine on next read (Inferred: or by a Go
-- backfill command, because the eligibility lives in the Definition, not in SQL).

-- +goose Down
DROP INDEX IF EXISTS uq_workflow_event_joined_once;
ALTER TABLE workflow_event DROP COLUMN scope_id, DROP COLUMN token_id;
DROP TABLE workflow_token;
DROP TABLE workflow_scope;
```

Down is only safe before any case has split (Inferred). Document that Down on a database with split cases loses branch state.

### RED tests (write first, observe failing)

| Spec | Tests |
|---|---|
| S1–S2 | `TestValidate_ParallelSplitRequiresMatchingJoin`, `TestValidate_JoinNamedByExactlyOneSplit`, `TestValidate_BranchMustNotReachTerminal`, `TestValidate_RejectsImproperlyNestedBlocks`, `TestValidate_RejectsReturnAcrossBlock`, `TestValidate_InclusiveSplitNeedsDefaultLast`, `TestValidate_ParallelSplitRejectsGuards`, `TestValidate_GatewaysRejectEligibilityDeadlineEffects`, `TestValidate_JoinHasExactlyOneUnguardedOutgoing`, `TestValidate_RejectsGatewayOnlyCycle` |
| S4 | `TestMove_ParallelSplitCreatesOneTokenPerBranch`, `TestMove_InclusiveSplitTakesEveryPassingRoute`, `TestMove_InclusiveSplitFallsBackToDefault`, `TestMove_EmptyBranchArrivesAtJoinInSameCall`, `TestMove_SplitAppendsSplitEventThenTokenHops` |
| S5–S6 | `TestMove_JoinWaitsUntilEveryBranchArrives`, `TestMove_JoinFiresOnceAndContinuesInParentScope`, `TestMove_NestedSplitsJoinInnerThenOuter`, `TestMove_InclusiveJoinWaitsOnlyForEmittedTokens` |
| S5 concurrency | `TestEngine_ConcurrentLastBranches_OneConflictThenSingleJoin` (fake, `racingRepository` style, `vogel/workflow/engine_test.go:1566`); `TestRepository_Integration_ConcurrentLastBranches_JoinExactlyOnce` (two pool transactions on testcontainers complete the last two branches at once: expect one commit, one `ErrConflict`, a retry that commits, and exactly one `joined` event); `TestRepository_Integration_DoubleSubmitSameToken` (one success, one `ErrConflict` or `ErrTokenNotFound`, exactly one successor token); `TestRepository_Integration_JoinedOnceIndexRejectsSecondJoin` |
| S7 | `TestMove_SingleTokenCaseNeedsNoTokenID`, `TestMove_SeveralActiveTokensRequireTokenID`, `TestMove_UnknownTokenIsNotFound`, `TestClaim_IsPerToken`, `TestRelease_IsPerToken`, `TestCaseProjection_StateEmptyWhileParallel` |
| S8 | `TestInbox_ReturnsOneWorkItemPerActiveToken`, `TestInbox_FiltersByEligibilityInSQL` (integration), `TestInbox_PagesTokensNotCases` |
| S9 | `TestReturn_InsideBranchUsesScopeHistoryOnly` |
| S10 | `TestCancel_RequiresReason`, `TestCancel_CancelsEveryTokenWithOneEventEach`, `TestCancelToken_CompletesJoinWhenOthersWait`, `TestCancelToken_RejectsLastBranch` |
| S11 | `TestDeadline_IsSetPerTokenOnEntry`, `TestInbox_OverdueIsPerToken` |
| S12 | `TestEvents_CarryTokenAndScope`, `TestListEvents_LegacyNullTokenReadsAsCaseID` (integration), `TestDispatch_CopiesTokenIDToArgs` (action) |
| S13 | `TestEngine_RandomWalk_InvariantsHold` (seeded random actions over generated block-structured definitions; asserts I1–I8 after each step) |
| S14 | `TestMigration007_BackfillsRootScopeAndToken` (open and closed cases), `TestEngine_SynthesizesRootTokenForCaseWithoutTokens` |
| S15 | `TestJoinCheck_RejectionAbortsMoveAndPersistsNothing`, `TestMoveResult_ReportsJoinedScopes` |

### Breaking changes (CHANGELOG "Cambiado (rompe)")

- `Engine.Inbox` returns `[]WorkItem`, and `Repository.ListByEligibility` is replaced by `ListWorkItems`.
- `Repository` gains scope and token methods.
- `Case.State`, `Case.AssignedTo` and `Case.DeadlineAt` are projections: they are empty or nil while a case is in a parallel region.
- `Status` gains `cancelled`, so consumers that switch on `Status` must handle it.
- Migration 007 is mandatory before deploying, and all binaries must be replaced together.
- Single-token definitions behave as before, and `MoveInput.TokenID` may stay zero for them.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S1 S2 | inline | done | RED (compile) → GREEN; `79f3cc6` |
| T2 | S3 + repository interface + fakes | inline | done | compile RED → GREEN; commit B |
| T3 | S4 S6 | inline | done | tests after code; mutation-checked (L8); commit B |
| T4 | S5 + concurrency tests | inline | done (integration pending CI) | fake race test green; postgres race/double-submit/index tests compile, not run (no Docker); commit B |
| T5 | S7 S9 S11 | inline | done | mutation-checked; commit B |
| T6 | S8 | inline | done (SQL pending CI) | fake inbox tests green; `TestInbox_FiltersByEligibilityInSQL` compiles; commit B |
| T7 | S10 | inline | done | commit B |
| T8 | S12 S15 | inline | done | engine part commit B; action `TokenID` RED → GREEN, commit C |
| T9 | S13 S14 (migration 008, shim, random walk) | inline | done (migration tests pending CI) | random walk 40 seeds × 3 cases green, mutation-checked; differential vs v0.18.0 identical (L9); commit B |
| T10 | S16 | inline | pending | — |

Risk: items 1 (persisted format, data migration), 3 (public API) and 4 (concurrency: join race) → an independent verifier for T4 and T9 at least. T4 and T9 each get their own commit so a review can take them alone.

## Log

- L1 2026-10-28 Oscar: "vogel es mío, vamos con la opción A, pero si es necesario corregir algo de vogel armame un strachpad con todo lo necesario y lo levanto en un worktree"
- L2 2026-10-28 Option A = token/work-item table inside vogel (`simple/docs/reverse-engineering/modules/01-case-trace.md` §6.3). Inputs that informed it: Zeebe element instances with flow scopes (token id = instance id), Flowable runtime executions plus history (same hybrid as vogel), Temporal append-only history plus mutable state.
- L3 2026-10-28 Design choices proposed by the handoff (change freely): block-structured gateways (split names its join) instead of free-graph joins with dead-path elimination; serialization through the existing case revision CAS instead of per-token locks; root token id = case id for backward compatibility; nullable `token_id` on events so the append-only triggers need no backfill.
- L4 2026-10-28 Open: whether SIMPLE processes need unstructured (non-block) parallelism or a "first finisher wins" discriminator. Neither is specified here; the production count of `paralelo`/`union` processes (01 open questions) decides whether they are ever needed.
- L5 2026-10-30 Oscar: "Hola!, Implementá @odd/tasks/workflow-tokens.md"
- L6 2026-10-30 Setup: the branch had no commits of its own and sat 40 behind origin/master; fast-forwarded to v0.18.0 (3ae0edc). Migration 007 already exists (`007_workflow_event_digest.sql`), so the token migration is `008_workflow_tokens.sql` and S14's "Migration 007" reads as 008. The digest of 007 covers 20 fixed columns; `token_id`/`scope_id` are not covered (covering them needs a new version tag, unrequested) — follow-up candidate. No Docker daemon on this machine: integration tests compile here and run in CI.
- L7 2026-10-30 Decisions while implementing: a token keeps its ID while it moves inside its scope (root token = case ID for life, per S3); a split consumes the arriving token and emits new ones; a join creates a new token in the parent scope. `EventJoined` carries `ScopeID` = the joined scope (for the unique index) and `TokenID` = the token it creates, so every new event has a non-NULL token (NULL stays reserved for legacy rows). `Token` gained `Eligible` (the stored `position`/`unit` of S8). `Tokens()` orders by `EnteredSeq` (no creation column). A closed case keeps `State` = terminal node and a cancelled one keeps its last projection (S7's projection applies while open; legacy tests pin the closed behavior). Cancelled cases answer `ErrCaseClosed` to Move/Claim/Cancel. `RefreshEligibility(ctx, db, caseID)` added: migration 008 cannot know `position`/`unit` (the draft flagged it as Inferred) and the engine also fills them on load, persisted on the next write. Validation also rejects gateways as start/terminal, returns from/into gateways and `Join` on non-splits ("like decision nodes", S2f). Commit plan changed: T2–T9 engine + repository + migration ship as one commit (B) because the `Repository` interface change does not build in slices; action `TokenID` is commit C; docs/boilerplate commit D.
- L8 2026-10-30 Test-first exception: T3–T7 engine code was written before its behavior tests. Evidence instead: 7 single-line mutations of run.go/engine.go (join fires on first arrival, occupiedStates ignores scope, pick takes first token, JoinCheck ignored, no projection, inclusive takes every route, last-branch cancel allowed) each fail at least one test; 3 of 4 mutations are caught by the random walk alone. I4 was refined while testing: "one token per branch" — tokens a nested split consumed are not counted, and an unjoined child scope stands for its branch (the literal "tokens created in it" is false for any scope containing a nested block or the root).
- L9 2026-10-30 Differential check: a scripted single-token run (decision, step, claim/double claim, inbox, return accepted and rejected, release, close, ops on the closed case) on v0.18.0 and on this tree prints identical output except for random case IDs. `go test -race ./...` green. Integration (`-tags integration`) compiles; not run here (no Docker daemon): `tokens_integration_test.go` and the existing suites need CI.
- L10 2026-10-30 Independent verifier (commits 79f3cc6..117f19b): S1–S7, S9–S15 pass (S5/S14 SQL by static review; integration not run, no Docker); S8 fail. B1: `ListWorkItems` ordered by `t.entered_at` instead of v0.18.0's `opened_at` — an unrequested reorder for single-token cases. Advisories: A1 `CancelToken` refused a branch whose sibling runs inside an unjoined nested block; A2 `RecordFailure` events carried no token (L7's "every new event has a non-NULL token" was false for them); A3 cancelled parallel case keeps `State = ""` (by design, L7); A4 shim cases invisible to the inbox until their next write (documented); A5 migration: `closed_seq` 0 for `last_seq` 0, 005's purge recipe blocked by the new FKs, Down leaves `cancelled` cases.
- L11 2026-10-30 Correction batch (one, per the bounds): B1 fixed — order is `c.opened_at, c.id, t.entered_seq, t.id` (fake mirrors it; RED `TestInbox_KeepsCaseOrderThenTokenOrder`, integration twin `TestInbox_KeepsCaseOrderInPostgres`). A1 fixed — an open child scope counts as a live sibling (RED `TestCancelToken_SiblingLiveInsideANestedBlockCounts`). A2 fixed — `FailureInput.TokenID` (optional; zero = case level, now stored as the case ID instead of NULL, read identically); boilerplate passes it. A5 fixed — `GREATEST(last_seq, 1)` for `closed_seq`, retention recipe and Down note in 008's header, `parent_id` FK is NO ACTION so one DELETE removes a scope tree. A3/A4 kept as documented. CHANGELOG uses the repository's existing "Cambios que rompen" heading for S16's "Cambiado (rompe)".
