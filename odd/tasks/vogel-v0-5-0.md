# Feature: vogel v0.5.0 — numeric query parsers, bounded decimals, struct validation

Locator: `odd/tasks/vogel-v0-5-0.md` (local, excluded via .git/info/exclude) · Engram topic `odd/vogel-v0-5-0/tasks`
Spec: `/tmp/claude-1000/-home-oscar-Work-go-crucible/9a9c0b72-538e-4433-aa10-4f1bd3d43b8d/scratchpad/vogel-v0.5.0-instructions.md`

## Objective
Let go-crucible (and every other consumer) drop its local `Int64Query`/`BoolQuery`/`DecimalQuery`
wrapper, `pkg/decimalutil`, and raw go-playground error output by shipping them in vogel v0.5.0.

## Why
The three pieces are duplicated per system; the raw `{"validation": err.Error()}` response leaks Go
struct/field names, has no per-JSON-field keys and cannot be localized.

## Constraints
- ADDITIVE ONLY: no v0.4.0 API change; `request/compat_test.go` golden tests stay green unchanged.
- `request` must NOT import shopspring/decimal.
- New direct deps only: `github.com/shopspring/decimal` v1.4.0, `github.com/go-playground/validator/v10` v10.30.1.
- Code/comments/docs identifiers English; repo prose (CHANGELOG, MIGRATION_GUIDE) Spanish.
- Conventional commits, NO AI attribution / Co-Authored-By (user CLAUDE.md).
- Tag v0.5.0 only after review.

## TDD
Mode: strict (enabled) · Source: session config ("Strict TDD Mode: enabled") · Runner: `go test ./...`
RED observed before implementation, then GREEN, then REFACTOR.

## Delivery
Strategy: ask-on-risk → user chose **stacked-to-main** (2026-09-22). Forecast ~1300 authored lines.
Slices (each its own branch + PR to master, merged in order):
- PR-A `feat/request-numeric-queries` → T1
- PR-B `feat/decimalx` → T2
- PR-C `feat/validation` → T3 + T4
Tag `v0.5.0` after PR-C merges (T5).

## Tasks
- [x] T1 request: `Int64Query`, `BoolQuery`, `Messages.InvalidBoolean`, `Messages.InvalidDecimal`, minimal exported hook for decimalx — route: delegated (writer trigger: messages.go + validate.go + tests)
- [x] T2 `decimalx` + `decimalxtest` (port crucible pkg/decimalutil, Bounds, ParseAmount/ValidateAmount, Query) — route: delegated (writer + 4+ source files)
- [x] T3 `validation` package (Struct → FieldErrors with JSON paths, Messages, Write) — route: delegated (writer)
- [x] T4 CHANGELOG + docs/MIGRATION_GUIDE.md — route: with T3 writer
- [x] T5 review, merge chain, tag v0.5.0 — route: inline

## Acceptance
`go test ./...`, `go vet ./...`, `golangci-lint run ./...` (~/go/bin/golangci-lint) pass; compat tests unchanged.

## Progress / evidence
- T1: commit 7fd60d7 on feat/request-numeric-queries; RED observed (undefined symbols), GREEN; go test -race/vet/gofmt/golangci-lint clean; compat_test untouched. RDD: medium, granted, approved (no findings), acknowledged. PR #4 MERGED as 4470a8f.
- T2: commits c3922d9 (feat) + 963a938 (fix: exponent ceiling MaxExponentLimit=1000, wider guard, 2s timing) + e051693 (fix: ErrInvalidScale, goroutine-safe timing, length-divergence doc, guard skips vendor/testdata). RED/GREEN each; go test -race/vet/gofmt/golangci-lint clean; request has no shopspring dep. RDD: 4 reviews, all approved+acked; last one covered whole branch. Kept crucible semantics: literal exponent bound (padding zeros rejected), Parse string-length vs ValidateBounds digits. PR #5 open. Follow-up: guard hidden-dir/explicit-base tests.

- T2 extra: f273ab8 fix MaxLengthLimit=1000 (RED/GREEN), review approved+acked; PR #5 body lists follow-ups (Unwrap doc, guard tests, 5s timeout).
- T3/T4: 0433968 feat(validation), a5351de docs, b3d0b68 fix (embedded flattening, required_* wording, typed-nil tests, MaxLengthLimit docs). RED/GREEN; -race/vet/gofmt/lint clean. Reviews: feat+docs approved+acked; whole-branch review of b3d0b68 stuck on false inferential CRITICAL (request API "missing" — verified present in base tree 0e4a1f52) → correction_required, reoffered START failed candidate_context_unavailable. User chose report_and_continue: filed gentle-ai#4886, ran exact declined invocation (validated declined_this_candidate). PR #6 open, base feat/decimalx.

- T5: PR #5 merged (9007cbd..d8ce008), feat/validation rebased and #6 retargeted+merged (4725988..a884c3d), release docs ead0020 pushed to master, CI green, annotated tag v0.5.0 on ead0020 pushed, resolved by proxy.golang.org.

## Next step
Done. go-crucible: go get vogel@v0.5.0 and follow MIGRATION_GUIDE (drop pkg/decimalutil, query wrapper, raw validator output). Follow-ups in PR #5/#6 bodies; gentle-ai#4886 upstream.
