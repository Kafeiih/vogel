# Feature: owasp-findings — remediate the 2026-10-09 OWASP audit

Locator: `odd/tasks/owasp-findings.md` (local, excluded via .git/info/exclude) · Engram topic `odd/owasp-findings/tasks`
Branch: `security/owasp-findings` (from master 39a519a)
Audit source: `~/Work/oscar/opesectechniques/security-audit-checklist.md`; findings in Engram obs 3986 (audit), 3995 (scans).

## Objective
Close the library and example findings from the OWASP checklist audit, one work-unit commit per finding,
with tests and CHANGELOG alongside each behavior change.

## Problem / Why
The audit found no SQL injection and strong headers, but several gaps that every consumer copies:
a lost-update race in the workflow engine, no `Content-Type` check in the JSON decoder, presigned S3
GETs that render uploads inline, dead `PresignExpiry` config, raw request payloads persisted immutably in
`audit_log`, silent 403s, and a boilerplate that trusts proxy headers from any client.

## Scope
Library packages (`request`, `httpx/middleware`, `storage/s3`, `audit`, `workflow`, `config`, `notification`)
plus `examples/boilerplate` and `examples/api` wiring, CI pinning, and docs. No new dependencies.

## Constraints
- Additive API where possible; any behavior change gets a CHANGELOG `[Unreleased]` entry.
- Test-first for every behavior change (RED observed, then GREEN). Runner: `go test ./<pkg>/... -race`.
- Artifacts in English; CHANGELOG in Spanish (repo convention); ci.yaml comments in Spanish without accents.
- No AI attribution in commits. Conventional Commits.
- Delivery budget: forecast ~850 authored lines > 400 → strategy `ask-on-risk` (pending user choice, see below).

## Tasks (ordered by severity then cost)

- [x] T1 `request`: reject non-JSON `Content-Type` with 415 and reject trailing data after the first JSON value.
      Evidence: `request/json.go:110-125`. Route: delegated (test + impl + messages). Checks: `go test ./request/... -race`.
      Done 2026-10-09: `AllowAnyContentType()` opt-out on `request.New`; trailing-data rule has no opt-out; compat golden
      test sets the header as setup (parent inline edit). Commit 0fd3381, +482/−56.
- [x] T2 `httpx/middleware/security`: add `Cache-Control: no-store` (and `Pragma: no-cache`) to `SecurityHeaders`.
      Evidence: `httpx/middleware/security.go:8-14`. Route: delegated with T3 (one writer). Checks: `go test ./httpx/... -race`.
      Done 2026-10-09; handlers can override since headers are set before `next`.
- [x] T3 `httpx/middleware/access`: log 403 denials with actor, resource, action, request id.
      Evidence: `httpx/middleware/access.go:107-109`. Checks: `go test ./httpx/... -race`.
      Done 2026-10-09: Warn "authorization denied" with user_id, resource_kind, resource_id, action, method, path, request_id
      (via `reqctx.RequestIDFromContext`); `WriteAccessError` logs with empty resource fields. T2+T3 commit: see table.
- [x] T4 `storage/s3`: wire `Config.PresignExpiry` as default when expiry <= 0; add `PresignGetOption`s for
      `ResponseContentDisposition` (default `attachment`) and `ResponseContentType`; extend the `storage` port.
      Evidence: `storage/s3/s3.go:28,152-157`, `storage/storage.go:59`. Checks: `go test ./storage/... -race`.
      Done 2026-10-09: options live in `storage/presign.go` (`ApplyPresignGetOptions` folds defaults so every adapter
      shares them); `AsAttachment` strips CR/LF/quote/backslash; non-ASCII names not RFC 5987 encoded (open).
- [x] T5 `audit`: redaction for `WithInputPayload` and snapshots (default denylist: password, token, secret,
      authorization, api_key, cookie; `WithRedactor` override). Evidence: `audit/recorder.go:64-69`, `audit/audit.go:100-105`.
      Checks: `go test ./audit/... -race`.
- [x] T6 `workflow`: make `Move`/`Claim`/`Release` conflict-safe: `Update ... WHERE id=$1 AND state=$expected AND status='open'`,
      0 rows → `ErrConflict`; document that callers run inside `pgxtx.WithTx`. Read `odd/tasks/workflow-decisions-comments.md`
      first (mentions transactional guards). Evidence: `workflow/engine.go:426-504`, `workflow/postgres/repository.go:92-96`.
      Checks: `go test ./workflow/... -race` (+ integration tag if testcontainers available).
- [x] T7 `examples/boilerplate`: gate `middleware.RealIP` behind a `TRUSTED_PROXIES` CIDR list (off by default),
      add a router test for malformed vs valid forwarded headers, verify httprate keying.
      Evidence: `examples/boilerplate/internal/interfaces/http/router.go:75`. Checks: boilerplate `go test ./... -race`.
- [x] T8 `examples/api`: stop binding `owner_id` from the client body; derive from the authenticated principal.
      Evidence: `examples/api/handler.go:91,150,280`. Checks: `go test ./examples/api/... -race`.
- [x] T9 `config`: redact URL userinfo in `ValidURL`/`OneOf` error text. Evidence: `config/validate.go:22,48`.
      Checks: `go test ./config/... -race`.
- [x] T10 `notification`: `Message.Validate` parses addresses with `net/mail` so SendGrid gets the same guarantee as SMTP.
      Evidence: `notification/notification.go:31-45`, `notification/sendgrid/sendgrid.go:55-56`. Checks: `go test ./notification/... -race`.
- [x] T11 docs: `docs/WIRING.md` + boilerplate RUNBOOK: run migrations under a separate migrator role; runtime role gets
      INSERT/SELECT only on `audit_log` (append-only trigger is owner-bypassable). Evidence: `audit/migrations/002_audit_log_append_only.sql:16-21`.
- [x] T12 CI: pin third-party actions to full commit SHAs. Evidence: `.github/workflows/ci.yaml` `uses:` lines.
      Verified done 2026-10-20 (boxes T5-T12 were stale): audit redaction in audit/recorder.go; workflow ErrConflict
      on a stale revision (workflow/workflow.go:665); TRUSTED_PROXIES in the boilerplate config; examples/api no longer
      binds owner_id; config/validate.go hides URL userinfo; notification uses net/mail; migrator role in the RUNBOOK;
      every workflow `uses:` pinned to a 40-char SHA.

Deferred (needs product decision, not in this feature): token revocation/introspection for Zitadel; Cerbos TLS default;
`postgres.RequireTLS` default; tenant model.

## Acceptance criteria
Each task: tests prove the new behavior, package tests pass with `-race`, CHANGELOG updated when behavior changes,
one Conventional Commit on the feature branch, RDD assess run on the commit and its outcome recorded here.

## Progress / evidence
| Task | Route | Commit | Checks | RDD |
| --- | --- | --- | --- | --- |
| T1 | delegated writer (trigger: 2+ non-trivial files, test-first) + parent 1-line compat fix | 0fd3381 | RED observed on 8 tests → GREEN; `go test ./request/... ./httpx/response/... -race` ok; vet clean; golangci-lint 0 issues; `go build ./...` ok | medium, `slice_budget_reached`; granted; approved + acknowledged (lineage review-a88d71e11b578c83). Advisory: single-value rule reads body to EOF (bounded by MaxBytes + server timeouts); add test for `AllowAnyContentType` composed with `WithMessages` and via `JSONWithLimit`. Reviewed boundary → 0fd3381 |
| T2+T3 | delegated writer (trigger: 2+ non-trivial files, test-first) | 7162f9e | RED on 4 tests → GREEN; `go test ./httpx/... -race` ok (parent re-ran); vet clean; golangci-lint 0 issues; build ok | high (`hot_path` security.go); granted; approved + acked (review-e31820d2f9d15b62). Advisory acted on in 0f14799: nil logger guard, 503 fields aligned, comment fixed. Open advisory: no sampling on 403 log bursts; `no-store` affects handlers relying on ETag revalidation (documented override). Reviewed boundary → 7162f9e |
| T3b (follow-up) | inline (1 file + test; RED observed via stash: nil-pointer panic) | 0f14799 | `go test ./httpx/... -race` ok; vet clean; lint 0 issues | medium, `under_budget` (67 lines): stays pending in the slice; boundary remains 7162f9e |
| T4 | delegated writer (trigger: 2+ non-trivial files, port change, test-first) | 3c69859 | RED: build failures on new symbols → GREEN 24 subtests; `go test ./storage/... -race` ok (parent re-ran); vet clean; lint 0 issues; root + boilerplate build ok | medium, `slice_budget_reached` (562 lines incl. 0f14799); granted; transaction review-5fe4bb7d35d24ee1 OPEN in state `reviewing` (lens review-reliability). Capture failed twice with `claude reviewer transport failed: session limit, resets 17:20 America/Santiago` (provider usage limit, not a Gentle AI defect). Resume: re-run the bound STATUS `gentle-ai review status --contract=gentle-ai.review-integration/v2 --next-transition=true --lineage=review-5fe4bb7d35d24ee1 --repository-context=rctx2_40e92c9cbd4b54f65af6e33bf813cf6367011848879e00c8032e852058e32a9b --agent=claude-code --base-ref=3700645fdcf8b949559052f0874d29b27156f341 --committed-only=true` from the repo root and run the capture it reoffers |

## Delivery
Strategy: `feature-branch-chain` (user chose 2026-10-09). Running count: 538 (PR-1) + 354 (PR-2 so far: 287 + 67).
Slices: PR-1 = T1 (0fd3381). PR-2 = 7162f9e, 0f14799, … cut when PR-2 passes ~400 (T4 will push it over → PR-3 starts after T4).

## Next step
1. Finish the open review of 3c69859 (see T4 row) once the usage limit resets; acknowledge if approved; boundary → 3c69859.
2. PR-2 = 7162f9e, 0f14799, 3c69859 (≈835 lines, over budget: cut PR-3 at T5). Running count total ≈1373.
3. T5 (`audit` redaction): design ready — `NewRecorder(repo, opts ...RecorderOption)` with `WithRedactedKeys` / `WithRedactor`,
   default denylist (password, token, secret, authorization, api_key, cookie), applied in `Record` before `repo.Create` to
   InputPayload, BeforeSnapshot, AfterSnapshot, ChangedFields (recursive, case-insensitive, value → "[REDACTED]").
