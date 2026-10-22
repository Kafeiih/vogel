# request-crucible-parity (vogel v0.4.0)

## Objective
Close the gaps that keep go-crucible on local forks of `vogel/request` and
`vogel/httpx/response`, without breaking v0.2.0/v0.3.0 consumers.

## Problem / why
Crucible's forks drift (e.g. the 1_048_578 byte-limit bug). It depends on
`JSONOptional`, `EXPORT_TOO_LARGE`, localized messages and a way to extend the
Validator with its own parsers.

## Scope
- `httpx/response`: `CodeExportTooLarge = "EXPORT_TOO_LARGE"`.
- `request`: `Messages`, `DefaultMessages`, `Decoder` via `New(opts...)`,
  `WithMessages`, `JSONOptional`, `Validator.AddError`; package-level funcs
  delegate to a default instance.
- CHANGELOG.md (new) + docs/MIGRATION_GUIDE.md crucible rows.

## Constraints
- Byte-identical responses with default options; existing tests untouched.
- No new dependencies. CSV, Int64Query, BoolQuery, DecimalQuery, go-playground stay in crucible.
- Deviation: no `MaxInt` message field. `MaxInt` only clamps (in vogel AND in
  crucible's fork) and never records an error; emitting one would change
  default behavior. A field nothing reads would be dead API.

## TDD
- Mode: strict, enabled. Source: user global CLAUDE.md ("Strict TDD Mode: enabled").
- Runner: `go test ./...`; plus `go vet ./...` and `~/go/bin/golangci-lint run ./...`.

## Tasks
- [x] T1 `CodeExportTooLarge` + test — route: inline (1 file + test, mechanical).
- [x] T2 `Messages`/`Decoder`/`WithMessages`/`JSONOptional`/`AddError` + tests —
      route: delegated writer (writer trigger: 3+ non-trivial files in `request/`).
- [x] T3 CHANGELOG.md + MIGRATION_GUIDE.md — route: inline (docs only).
- [x] T4 Merge (ff) + tag `v0.4.0` + push — user-authorized. Tag 3e3a836 -> 2b053c3, pushed to origin.

## Acceptance
go test/vet/lint green; new tests: CodeExportTooLarge value, JSONOptional x4,
WithMessages decode + validator override, empty-field fallback, AddError in
Errors()/WriteErrors.

## Delivery
Strategy: ask-on-risk. Forecast ~450 authored lines (mostly tests).

## Progress / evidence
- Engram mirror: PENDING (mem_save refused: multiple active runtime sessions).
- T1 c13503c — RED: undefined CodeExportTooLarge; GREEN go test; lint 0 issues. Assess: medium/under_budget.
- T2 039f623 (delegated writer) — RED: 11 compile errors; go test ./..., go vet, golangci-lint 0 issues; existing tests untouched (git diff empty). Commit amended to drop an AI attribution line the writer added.
- T3 f233a2f — CHANGELOG.md (new), MIGRATION_GUIDE crucible rows + request section.
- Review: slice 05baf56..f233a2f due (medium, slice_budget_reached, 627 lines). STOPPED before consent: after the
  intended-untracked selection, the returned START targeted the uncommitted workspace (.atl/ registry files) instead
  of the committed slice. Not relayed.
- Review retry: added `odd/` to .git/info/exclude (local) to avoid the untracked selection. START target = base-diff
  of the 9 slice files. User granted. Lens review-reliability -> APPROVED, acknowledged
  (gentle-ai.review-acknowledged/v1, lineage review-2b69d327bc594d5c). Reviewed boundary advances to f233a2f.
  3 informational SUGGESTIONs: tautological compat guard (messages_test.go:95-115), only 2/16 defaults pinned,
  JSONOptional 'untouched' not proved + missing whitespace/truncated/localized cases.
- T5 b5c359b test(request): golden v0.3.0 bytes for all 6 decode branches + 13 Validator messages (pass on v0.3.0
  worktree too), JSONOptional preset/whitespace/truncated/localized. Characterization tests: no natural RED;
  mutation check instead (EmptyBody text + ErrUnexpectedEOF-as-empty both caught). Assess vs f233a2f: medium/under_budget.
- Hook review of full branch 05baf56..b5c359b: granted, APPROVED, acknowledged (review-8c07c82f7c7e82d6).
  WARNING R3-001 zero-value Decoder panics; SUGGESTION R3-002 only 3/16 WithMessages fields tested.
- T6 d038627 fix(request): uninitialized Decoder (zero/nil) falls back to DefaultMessages; table test for all 16
  fields. RED: nil-func panic reproduced; GREEN: go test/vet/lint 0 issues. Assess vs b5c359b: medium/under_budget.
- .atl/ registry churn discarded twice (user decision); the hook regenerates it.
- Hook review of full branch 05baf56..d038627: granted, APPROVED, acknowledged (review-d3fef375ad8d1310).
  1 SUGGESTION: AddError overwrite semantics untested (not applied; offered to user).
- T7 test(request): AddError last-write-wins (repeated + after built-in parser). Characterization; mutation
  (first-write-wins) caught. go test/vet/lint 0 issues.
- Hook review of full branch 05baf56..a8d42a1: granted, APPROVED, acknowledged (review-99a5d5e10d13e672).
  2 SUGGESTIONs (not applied): zero-value Validator panics (pre-existing in v0.3.0); Option applied to a
  zero Decoder is silently discarded (Option is func(*Decoder), exported).
- dc0346b docs bump to v0.4.0; 2b053c3 JSONOptional doc fix (reviews approved: review-d2e6902008b3dd2b, review-85441b54c6f1d4db).
- Remaining suggestion (not applied, no real consumer path): zero-value Validator panics in AddError (pre-existing).
## Next step
Done. go-crucible can adopt v0.4.0; go-licencias BoolQuery could switch to v.AddError.
