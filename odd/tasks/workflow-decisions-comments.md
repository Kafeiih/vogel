# Feature: workflow — decision nodes, step comments, transactional guards, return transitions

Locator: `odd/tasks/workflow-decisions-comments.md` (local, excluded via .git/info/exclude) · Engram topic `odd/workflow-decisions-comments/tasks`
Driven from: sistema-compras design session (2026-09-22), first consumer of `vogel/workflow`.

## Objective
Extend `vogel/workflow` so a purchasing process (and future mini-BPMs) can route on domain data,
record the "why" of each step, and send a case back to an earlier decision stage.

## Problem / Why
- A `Transition` fuses the human action with its destination; `Validate` forbids two transitions
  sharing `(From, Action)`, so data-driven routing (e.g. "any item has subsidy SEP") is impossible.
- Steps carry no comment/observation; users need an optional comment on approve and a required
  observation on reject as part of the case history.
- `GuardFunc(ctx, Case)` gets no transaction, so a guard reading domain data cannot see writes made
  earlier in the caller's transaction.
- Bureaucratic stages must be able to return a case to any earlier decision stage the case actually
  visited (e.g. never offer "return to Coordinador SEP" when the case skipped it).

## Scope (authorized)
Package `workflow`, `workflow/postgres`, `workflow/migrations`, README + CHANGELOG entries.
Out of scope: any sistema-compras / go-safo code; tagging a release (user decision).

## Constraints
- Engine stays domain-blind (no domain data in Case/Event beyond reference + comment text).
- Migration 001 is released (v0.5.0): never edit it; schema changes go in `002_*.sql`.
- `GuardFunc` signature change is a breaking change (allowed in v0.x, no consumers yet) → CHANGELOG.
- Code/comments/identifiers English; repo prose (CHANGELOG, README) Spanish, matching existing files.
- Conventional commits, NO AI attribution / Co-Authored-By (user CLAUDE.md).
- ~400 authored lines per task is advisory only.

## TDD
Mode: strict (enabled) · Source: session config ("Strict TDD Mode: enabled") · Runner: `go test ./...`
(integration: `workflow/postgres/repository_integration_test.go` — check its build tag/env).
RED observed before implementation, then GREEN, then REFACTOR.

## Design decisions
- D1 `GuardFunc func(ctx context.Context, db pgxtx.DBTX, c Case) (bool, error)`; engine passes the
  `db` it received.
- D2 `Transition.Comment CommentPolicy` (`""`/`none` default, `optional`, `required`);
  `MoveInput.Comment string`; `Event.Comment string`; `ErrCommentRequired`; comment on a `none`
  transition is rejected (`ErrCommentNotAllowed`) so the policy is honest. Stored in
  `workflow_event.comment TEXT NOT NULL DEFAULT ''` via migration 002.
- D3 `Node.Kind NodeKind` (`""`/`task` default → today's behavior, `decision`). Decision node: never
  start/terminal, no Eligible/Deadline; its outgoing transitions are routes evaluated in declaration
  order, first true guard wins; exactly one unguarded route, and it must be last (default).
  Duplicate `(From, Action)` still rejected (route labels unique). No cycle made only of decision
  nodes. On Move into a decision node the engine keeps routing in the same call until it rests on a
  task/terminal node; each hop appends an `EventMoved` whose Action is the route label and whose
  ActorID is the original actor. Comment policy does not apply to routes (validation rejects it).
  `Available` on a task node is unchanged; a case can never rest on a decision node.
- D4 `Transition.Return bool`: the transition is offered/allowed only if the case previously occupied
  `To` (read from its event history). Combines with `Guard` (both must pass). `ErrReturnNotVisited`.

## Delivery
Strategy: ask-on-risk; chain strategy reused from the user's last vogel feature: **stacked-to-main**
(user may change it). Forecast ~900 authored lines. Slices (local branches; push/PR = user decision):
- PR-A `feat/workflow-guard-tx` → T1
- PR-B `feat/workflow-step-comments` (base PR-A) → T2
- PR-C `feat/workflow-decision-nodes` (base PR-B) → T3 + T4

## Tasks
- [x] T1 D1 transactional guards — route: delegated (writer trigger: workflow.go + engine.go + tests)
- [x] T2 D2 step comments + migration 002 + postgres repo — route: delegated (writer: 4+ files)
- [x] T2b fix 3 WARNING advisories from T2's review (own work unit, new candidate) — route: delegated
- [x] T3 D3 decision nodes (validation + auto-routing) — route: delegated (writer)
- [x] T4 D4 return transitions — route: delegated (writer)
- [x] T4b close the return-vs-decision-node gap from T4's review — route: delegated
Each task carries its README/CHANGELOG entry in the same commit.

## Acceptance
`go test ./...`, `go vet ./...`, `gofmt -l`, `~/go/bin/golangci-lint run ./...` clean; integration tests
green when the DB env is available (else reported as skipped).

## Progress / evidence
- T1: commit 9a71858 `feat(workflow)!: pass the caller's transaction to guards` on feat/workflow-guard-tx
  (+91/-8, 5 files). RED: compile failure on new GuardFunc signature (engine_test.go:229). GREEN:
  TestMove_/TestAvailable_GuardReceivesCallersDB pass. go build/test/vet, gofmt -l, golangci-lint (0 issues)
  clean; integration tests ran on testcontainers and passed. Parent spot check: go test ./workflow/... ok,
  no attribution in commit. RDD assess: high (process_boundary signal in engine_test.go), review_due →
  consent relayed to user → granted. 4 lenses captured concurrently; result APPROVED with 0 blocking
  findings and 3 non-blocking SUGGESTIONs, acknowledged and burned (lineage review-070516e41155a3c2).
  Advisory follow-ups (separate later work, never reopen this candidate):
  - R4-guard-tx-abort-doc (workflow/workflow.go:365-373): document that a guard must return, not swallow,
    db errors and must close any pgx.Rows, else the caller's tx is poisoned (SQLSTATE 25P02).
  - R3-guard-tx-contract (workflow/workflow.go:364-373) and R2-test-implicit-guard-wiring
    (workflow/engine_test.go:392-435): readability/reliability suggestions.
  Decision: fold R4 (and R3 if it overlaps) into T2's doc pass, since T2 already touches this contract.

- T2: commit 84f58f9 `feat(workflow): record a comment policy and step comments on events` on
  feat/workflow-step-comments (+335/-20, 9 files). RED: undefined CommentPolicy/Transition.Comment/
  MoveInput.Comment/ErrCommentRequired/ErrCommentNotAllowed. GREEN: full suite ok; integration 7/7 with
  testcontainers (new TestRepository_AppendEvent_And_ListEvents_PersistsComment proves migration 002
  applies via migrations.FS()). go build/test/vet/gofmt/golangci-lint (0 issues) clean. Parent spot check
  ok. RDD: high → consent granted → APPROVED, acknowledged (lineage review-8f8db9d7d5be73cb).
  Advisories: 3 WARNING + 4 SUGGESTION → T2b.
  - R4-schema-before-code (CHANGELOG): the wording implies consumers who don't use comments may skip
    migration 002, but eventColumns always includes `comment`, so every AppendEvent/ListEvents (hence
    Move/Claim/Release/History) fails on an unmigrated DB. Must say: run 002 before deploying this version.
  - R2-none-alias-magic-string (workflow.go:98-108): "none" is a duplicated literal in Valid()/isNone();
    normalize in Validate or give it a named constant.
  - R3-comment-before-guard-unproved (engine.go:311-317): no test proves checkComment runs before the
    guard; add a call-counting guard on a CommentRequired/CommentNone transition asserting 0 calls.

- T2b: commit 6db40ff `fix(workflow): normalize the comment policy alias and correct the migration note`
  (+134/-11, 4 files). W1 CHANGELOG now says migration 002 is mandatory for every consumer before
  deploying this version. W2 `commentPolicyNoneAlias` + `Canonical()`, `isNone()` removed (Validate has a
  value receiver, so normalization happens at the comparison site). W3 call-counting guard tests prove the
  comment check precedes the guard — the writer verified genuine RED by temporarily swapping the order,
  then reverting engine.go byte-for-byte. All checks clean, integration 10.4s ok. RDD: high → granted →
  APPROVED, acknowledged (lineage review-49d5259ce51917d1).
  Carry-over advisory into T3 (same test file): R3-positional-guard-attachment — commentGuardTestDefinition
  attaches guards by slice index (Transitions[0]/[2]) instead of by Action, so a reordering would silently
  stop proving the ordering. Fix by looking transitions up by Action and asserting their comment policy.

- T3: commit 58885f4 `feat(workflow): route cases through decision nodes` on feat/workflow-decision-nodes
  (+927/-22, 6 files). NodeKind (task default | decision), decision validation rules, Move restructured as
  plan-then-persist (planMove/resolveRoute, maxDecisionHops=32) so a mid-chain failure mutates nothing;
  ErrNoRoute + ErrTooManyDecisionHops. Also fixed T2b's carry-over (transitions looked up by Action).
  RED: compile errors, then real assertion failures on engine behavior. All checks clean, integration
  10.7s ok, lint 0 issues (also with --build-tags=integration). RDD: high → granted → APPROVED,
  acknowledged. Advisories carried into T4 (same files):
  - R2-close-event-action-undocumented (engine.go:412-417): when a decision hop lands on a terminal node,
    EventClosed takes the LAST route's Action, not the human MoveInput.Action. Undocumented, untested, and
    the CHANGELOG claims closing works "exactamente como hoy". Document it, test it, fix the wording.
  - R3-same-timestamp-hop-events (engine.go:405-409): every hop of one Move shares one timestamp. Verified
    by the parent: workflow/postgres ListEvents orders by `seq ASC` (repository.go:133), so ordering is
    safe — but nothing proves it. Add a test asserting per-hop seq increases, and document the tie-break.

- T4: commit eeaf51b `feat(workflow): allow returning a case to a previously visited node` on
  feat/workflow-decision-nodes (+594/-8, 6 files). Transition.Return, ErrReturnNotVisited, occupiedStates
  from the case's own history, Available filters unvisited returns, check order Comment → Return → Guard,
  two Validate rules (no return out of a decision node, no return into a terminal node). History is read
  at most once per call, gated by hasReturnTransition and proved by call-counting repository tests. Both
  T3 advisories folded in (EventClosed.Action documented + tested, strictly increasing Seq tested,
  CHANGELOG wording corrected). All checks clean, integration 8/8 in 11.0s, lint 0 issues both tag sets.
  RDD: high → granted → APPROVED, acknowledged.
- T4 review advisories → T4b (both point at the same gap):
  - R3-return-decision-node-occupied (engine.go:323-325): automatic decision hops append EventMoved, so
    occupiedStates counts decision nodes as occupied. Validate only forbids returns FROM a decision node
    or INTO a terminal node, so a return INTO a decision node is accepted and would re-run routing —
    a "return" that can push the case forward. Untested, unproved.
  - R2-return-decision-stage-term (workflow.go:250-252 + CHANGELOG + README): the docs say a return goes
    back to a "decision stage", but "decision" already names the automatic gateway node kind here. The
    fixtures' return targets are task nodes. Terminology must not borrow the consumer's domain vocabulary.
  - Parent decision: REJECT a return that targets a decision node (Validate), and reword the docs to
    "an earlier task node the case already occupied". Rationale: the consumer never needs it (it returns
    to Territorial, a task node, and re-routing happens naturally on the way forward), and allowing it
    turns a return into something that can move the case forward — a foot-gun for one unused capability.

- T4b: commit fb770f6 `fix(workflow): reject a return that targets a decision node` (+72 lines, 5 files).
  RED: the new Validate subtest failed with "An error is expected but got nil". GREEN after the rule.
  Terminology reworded in doc comment, CHANGELOG and README; occupiedStates' misleading comment fixed.
  All checks clean, integration 8/8, lint 0 issues on both tag sets. RDD assess: medium, review_due=false
  (under_budget) → no review; boundary stays pending for the next commit in this slice.

## Delivery (2026-09-22) — DONE
All three slices merged to master as squash commits, CI green on each:
- PR #7 → cfecf22 (guard tx) · PR #10 → 86acdb9 (comments; replaced PR #8) · PR #9 → ac020d6 (decisions + returns)
Gotcha that cost a PR: `gh pr merge #7 --squash --delete-branch` deleted the base branch, which CLOSED
child PR #8 — a closed PR whose base is gone can be neither reopened nor retargeted, so it was recreated
as #10. Correct order for a stack: merge the parent WITHOUT --delete-branch → `gh pr edit <child> --base
master` → `git rebase --onto origin/master <old-base-tip> <child>` + force-with-lease → merge → then
delete branches.
master verified after the merges: go test ./... (25 ok), vet, gofmt, golangci-lint 0 issues.
Released: 0182b14 `docs: point adoption instructions at v0.6.0` (CHANGELOG header [Sin publicar] →
[0.6.0] — 2026-09-22, MIGRATION_GUIDE, examples/boilerplate README + go.mod pin), CI green on master,
annotated tag v0.6.0 on 0182b14 pushed and resolved by proxy.golang.org
(Hash 0182b14870532dd950d63f179c2f5ee1b19cadba). The boilerplate example has a local `replace`, so its
pin never gated CI. FEATURE CLOSED.

## Branch state before delivery (2026-09-22)
master..feat/workflow-decision-nodes = 6 commits, +2079/-43 over 10 files, full suite green.
Local only. Push, PRs and the v0.6.0 tag are the user's decisions.
Stack: feat/workflow-guard-tx (9a71858) → feat/workflow-step-comments (84f58f9, 6db40ff)
→ feat/workflow-decision-nodes (58885f4, eeaf51b, fb770f6).

## Next step
Engine work is DONE. Next is sistema-compras: the domain model (Solicitud with one budget account,
Item with subsidy + origin catalog/manual + movement trace, catalogs per account with snapshot on the
item) and the P01/P02 Definitions. Domain questions still open: Director/encargado and can_purchase,
the "Verificación Presupuesto" detour, aprobar_y_finalizar / rechazar_y_cerrar, and how a joint
purchase is created.
