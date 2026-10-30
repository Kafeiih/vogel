Objective: give `vogel/workflow` the administrative and time-driven operations a public-sector BPMS needs, each recorded in the trace: reassign, release with reason, change a deadline, expire with an optional timeout transition, business-calendar deadlines, optimistic `ExpectedRevision` on every input, and one helper that maps engine errors to HTTP status codes. Target v0.20.0 (additive except where noted). Feature 4 of 4. Depends on `workflow-trace-metadata.md` (actor, `Detail`, input structs) and `workflow-tokens.md` (per-token operations).
Branch: feat/workflow-case-operations from master after v0.19.0. Delivery: ask-on-risk, stacked-to-main (PR-A T1-T3, PR-B T4-T6). Test runner: `go test -race ./...`, `go test -tags integration ./workflow/...`, `cd examples/boilerplate && go test ./...`, `cmd/create-vogel-app/check-generated.sh`.

## Specs

Source audits: `simple/docs/reverse-engineering/modules/01-case-trace.md` §5.1 TR-9, TR-10, TR-12, TR-15, TR-17, TR-19, TR-24; §5.2 A2 (engine ops part), A5, A6, A7; E-D7. `modules/02-case-data.md` §6.9 (offline submission with `expectedRevision`). The same E-D7 defect exists in vogel's own template: `vogel/examples/boilerplate/internal/interfaces/http/handler/process_handler.go:308-326` maps no `ErrNoRoute`, `ErrGuardRejected`, `ErrCommentRequired`/`ErrCommentNotAllowed` or `ErrReturnNotVisited`, so they fall into the 500 branch.

- **S1 Reassign.** `Engine.Reassign(ctx, db, ReassignInput{CaseID, TokenID, To string, Actor Actor, Reason string, ExpectedRevision *int64})`:
  - `Reason` is required (`ErrReasonRequired`, defined in `workflow-tokens.md`). `To` must be non-empty.
  - It appends `assigned` with `AssignmentDetail{From, To, Reason}` and never dispatches effects, matching `Claim` today (`vogel/workflow/engine.go:748-773`), so entry actions don't re-run (TR-10).
  - Whether `To` holds the node's eligibility is the caller's check, like `Claim`. The engine never resolves people (`vogel/workflow/workflow.go:14-20`).
- **S2 Release by someone else.** `ReleaseInput.Reason` is required when `Actor.ID != token.AssignedTo` (`ErrReasonRequired`) and optional for a self-release. Today `Release` takes no reason and records no previous assignee (`vogel/workflow/engine.go:778-797`).
- **S3 Deadline change.** `Engine.SetDeadline(ctx, db, SetDeadlineInput{CaseID, TokenID, DeadlineAt *time.Time, Actor, Reason, ExpectedRevision})`: `Reason` is required. It appends `deadline_changed` with `DeadlineDetail{From, To *time.Time; Reason}`. A nil deadline removes it. It also clears any `expired` mark for that token (S5), so a token whose deadline was extended can expire again.
- **S4 Business-calendar deadlines.**
  - `Node.DeadlineRule string` names a rule resolved by a new port `Config.Deadlines DeadlineResolver` with `Due(ctx, DueInput{Rule string; From time.Time; Case Case; Token Token}) (time.Time, error)`.
  - `Definition.Validate` rejects `Deadline` and `DeadlineRule` set together. `Engine.Register` rejects a `DeadlineRule` when no resolver is configured (`ErrUnknownDeadlineRule`), mirroring `ErrUnknownEffect` (`vogel/workflow/engine.go:162-176`).
  - vogel ships `workflow/calendar` with `BusinessDays(n int, loc *time.Location, h Holidays) DueFunc` and a `Holidays` port `IsHoliday(ctx, civil date) (bool, error)`. The timezone is always explicit (TR-17). Holiday data is the consumer's, not vogel's.
  - The resolver runs inside the tx, so it must not call the network. This is the same rule as guards (`vogel/workflow/workflow.go:750-755`) and is documented on the port.
- **S5 Expire.** `Engine.Expire(ctx, db, ExpireInput{CaseID, TokenID, Actor})`:
  - `Actor.Kind` must be `ActorSystem`, else `ErrInvalidActor`. It fails with `ErrNotOverdue` unless `token.DeadlineAt <= now`.
  - It appends `expired` (`ExpiryDetail{DeadlineAt}`) once per deadline. A second call fails with `ErrAlreadyExpired`, so a scheduler retry is idempotent.
  - If the node declares the new `Node.OnExpire string` (the `Action` of one of its outgoing transitions; `Validate` checks it exists and is not a `Return`), Expire then moves the token through that transition in the same tx. The system actor is used, and `Event.Detail.cause = "expired"`.
  - Without `OnExpire` the token stays where it is, visibly overdue.
- **S6 Overdue listing.** `Engine.Overdue(ctx, db, OverdueFilter{Tenant string; Limit int; Before time.Time}) ([]WorkItem, error)` returns active tokens with a deadline before `Before` and no `expired` event for that deadline, oldest first, using `idx_workflow_token_deadline` from `workflow-tokens.md`. The periodic job that calls `Overdue` then `Expire` belongs to the consumer, scheduled as a River periodic job (open question in the handoff README: ship it in `action` instead?).
- **S7 ExpectedRevision.** `MoveInput`, `ClaimInput`, `ReleaseInput`, `ReassignInput`, `SetDeadlineInput` and `CancelInput` gain `ExpectedRevision *int64`. When it is set and differs from the stored revision, the call fails with `ErrConflict` before any other check or write. This lets offline or long-form clients detect a stale view without re-reading.
- **S8 HTTP mapping helper.** New package `workflow/workflowhttp` (it depends on `httpx/response`, so the root `workflow` package stays dependency-free) with `func Status(err error) (status int, code string, ok bool)`:
  - 404: `ErrCaseNotFound`, `ErrTokenNotFound`.
  - 409: `ErrConflict`, `ErrAlreadyAssigned`, `ErrCaseClosed`, `ErrInvalidTransition`, `ErrReturnNotVisited`, `ErrNoRoute`, `ErrGuardRejected`, `ErrLastBranch`, `ErrAlreadyExpired`, `ErrNotOverdue`, `ErrJoinRejected`.
  - 422: `ErrCommentRequired`, `ErrCommentNotAllowed`, `ErrReasonRequired`, `ErrTokenRequired`, `ErrInvalidActor`.
  - 400: `ErrOffsetOutOfRange`.
  - Everything else, including `ErrInvalidDefinition`, `ErrUnknownEffect` and `ErrTooManyDecisionHops`: `ok = false`, so the caller logs it and returns 500.
  
  The codes come from the closed catalog in `vogel/httpx/response`. A test enumerates every exported `Err*` of `workflow` from a hand-maintained list and fails when the list and the package diverge, so a new sentinel can't silently become a 500. The boilerplate's `fail` uses it first, which closes E-D7 in the template.
- **S9 Docs.** README row for `workflow/workflowhttp` and updated `workflow` row, CHANGELOG `Agregado` (Spanish) and the boilerplate wiring (`fail` via `workflowhttp.Status`).

### RED tests

| Spec | Tests |
|---|---|
| S1 | `TestReassign_RecordsFromToReasonAndDispatchesNoEffects`, `TestReassign_RequiresReason`, `TestReassign_PerToken` |
| S2 | `TestRelease_ByOtherActorRequiresReason`, `TestRelease_SelfReleaseNeedsNoReason` |
| S3 | `TestSetDeadline_RecordsOldAndNew`, `TestSetDeadline_ExtensionAllowsExpiringAgain` |
| S4 | `TestValidate_RejectsDeadlineAndRuleTogether`, `TestRegister_RejectsRuleWithoutResolver`, `TestCalendar_BusinessDaysSkipsWeekendsAndHolidays`, `TestCalendar_EvaluatesInExplicitLocation` (deadline near midnight America/Santiago vs UTC), `TestOpen_AppliesDeadlineRuleToStartToken` |
| S5 | `TestExpire_BeforeDeadlineIsNotOverdue`, `TestExpire_RecordsExpiredOnce`, `TestExpire_TakesOnExpireTransitionAsSystem`, `TestExpire_RequiresSystemActor`, `TestValidate_OnExpireMustNameOutgoingNonReturnTransition` |
| S6 | `TestOverdue_ListsOnlyUnexpiredOverdueTokens` (integration), `TestOverdue_PagesOldestFirst` |
| S7 | `TestMove_ExpectedRevisionMismatchIsConflictAndPersistsNothing`, `TestClaim_ExpectedRevisionMatchProceeds` |
| S8 | `TestStatus_MapsEveryWorkflowSentinel`, `TestStatus_WrappedErrorsAreMapped`, `TestStatus_UnknownErrorIsNotMapped`, boilerplate `TestProcessHandler_NoRouteIsConflictNot500` |

### Breaking changes

None for callers of v0.19.0, except that the boilerplate's handler error mapping changes status codes from 500 to 409/422 for the listed sentinels. This is intended and listed under `Corregido`.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S8 (can start right after `workflow-trace-metadata.md`; tokens add their sentinels later) | inline (Oscar) | done | `94761b3`; RED: boilerplate `TestProcessHandler_NoRouteIsConflictNot500` answered 500; drift guard proven by deleting one list entry |
| T2 | S1 S2 | inline (Oscar) | done | `0d82274`; RED: `TestRelease_ByOtherActorRequiresReason` got nil |
| T3 | S7 | inline (Oscar) | done | `0d61807`; RED: compile (no `ExpectedRevision` field) |
| T4 | S3 S5 S6 | inline (Oscar) | done (integration test pending CI) | `720cd9c`; RED: compile; drift guard fired for `ErrAlreadyExpired`/`ErrNotOverdue` |
| T5 | S4 (`workflow/calendar`) | inline (Oscar) | done | `f56e127`; RED: compile |
| T6 | S9 | inline (Oscar) | done | `16bffaa`; lint follow-up `ae407e0` |

Risk: item 3 (public API) and item 6 for the calendar (timezone edge cases), covered by the explicit-location test.

## Log

- L1 2026-10-28 Oscar: "vogel es mío, vamos con la opción A, pero si es necesario corregir algo de vogel armame un strachpad con todo lo necesario y lo levanto en un worktree"
- L2 2026-10-28 Derived from `simple/docs/reverse-engineering/modules/01-case-trace.md` §5.2 A2/A5/A6/A7 and E-D7, and `modules/02-case-data.md` §6.9. Admin authorization (Cerbos attributes, tenant checks) and `audit_log` rows for these operations stay in engorro.
- L3 2026-10-30 Oscar: "Implementá @odd/tasks/workflow-case-operations.md"
- L4 2026-10-30 Branch fast-forwarded to v0.19.0 (`a2459f3`) before the first write; it was created from an older master.
- L5 2026-10-30 Decisions taken while implementing:
  - The expired mark is a token column, `workflow_token.expired_deadline_at` (migration 009), holding the deadline Expire recorded. A token is expired while it equals `deadline_at`, so "once per deadline" holds and an older binary that moves a token cannot leave a stale mark behind. S6's `Overdue` filters on it instead of a NOT EXISTS over `workflow_event`.
  - S7 "before any other check": pure input validation (actor, reason) still runs before the case is read; the revision is compared right after the read, before any state check or write.
  - S5: Expire skips the `OnExpire` transition's CommentPolicy (the system has nothing to comment) but runs its Guard; a rejection fails Expire with `ErrGuardRejected`. Effects are dispatched only when Expire moved the token. Validate also rejects `OnExpire`/`DeadlineRule` on gateways.
  - S4: `calendar.BusinessDays` deadline = 00:00 (in `loc`) of the day after the n-th business day; the day of `from` never counts. `calendar.Rules` resolves names and returns `ErrUnknownDeadlineRule` for an unknown one at runtime; `Register` only checks that a resolver exists.
  - S8: unlisted sentinels (`ErrCaseExists`, `ErrNotAssigned`, `ErrUnknownDeadlineRule`, ...) stay unmapped (500), per the spec. `ErrTokenRequired` moves from 409 to 422 in the boilerplate.
  - S2 changes two existing tests that released someone else's claim without a reason (`TestReleaseWith_EmptyActorIDIsStillAllowed`, the random walk); they now pass a reason. The deprecated `Release` wrapper only releases the caller's own claim.
  - `Repository` gains `ListOverdue` and `Token.ExpiredDeadline`: third-party implementers must add them (CHANGELOG `Cambiado`).
- L6 2026-10-30 Verification: `go test -race ./...` OK; boilerplate `go test ./...` OK; `cmd/create-vogel-app/check-generated.sh` OK; golangci-lint v2.13.2 (`--build-tags=integration`) 0 issues on both modules after indexing `Node` loops (the two new fields pushed `Node` to 152 bytes, past rangeValCopy). `go test -tags integration ./workflow/...` NOT run: the local user cannot reach the Docker socket; `TestOverdue_ListsOnlyUnexpiredOverdueTokens` and migration 009 run first in CI.
- L7 2026-10-30 Follow-ups: a rule-resolved deadline is not recorded in any event, so replaying the history cannot rebuild it (the replay invariant only covers `Node.Deadline`); consider recording the entered deadline on `moved`/`opened`. Consider mapping `ErrNotAssigned`/`ErrCaseExists` (client-caused, unmapped by spec).
- L8 2026-10-30 Independent verifier: S1-S9 pass, no blockers (differential run against `a2459f3` changed only the two S2 tests). Native review (lineage `review-2d35376342397caa`, one reliability lens) approved and acknowledged. Advisories fixed: `calendar.BusinessDays` now honors ctx and fails after 366 consecutive non-business days instead of looping forever. Open advisories (follow-ups): a token whose `Expire` always fails stays at the head of `Overdue` (oldest first, no offset) and can starve newer ones; `Overdue` lists `deadline_at < Before` while `Expire` accepts `deadline_at <= now`; a `DeadlineRule` on a terminal node is ignored but still requires a resolver; rule deadlines are not replayable (L7).
- L9 2026-10-30 Oscar ran `go test -tags=integration -race -run 'TestOverdue|TestMigration' ./workflow/postgres/ -v` locally: all pass, `TestOverdue_ListsOnlyUnexpiredOverdueTokens` included (migration 009 applied by `startTestPool`). No Up/Down round-trip test exists for 009 yet.
