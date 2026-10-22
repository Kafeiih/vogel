# Feature: owasp-checklist-cycle2 — PAUSED 2026-10-14 (laptop); resume on desktop with chained PRs

Locator: odd/tasks/owasp-checklist-cycle2.md (git-excluded). Branch security/owasp-checklist-cycle2 pushed at b351840 (no PR yet). Base master 3d00e6c. Last reviewed boundary 0c3f826; 0c3f826..b351840 (97 lines, medium) unreviewed, rides in the next slice/PR.

## Resume (desktop)
1. git fetch && git switch security/owasp-checklist-cycle2 (recreate this file from the Engram mirror odd/owasp-checklist-cycle2/tasks; it is git-excluded).
2. Load chained-pr + work-unit-commits skills; plan chained PRs (~7.5k lines vs master, 34 commits). Natural slices: C8a library (c33db9b..54614a1), follow-ups + C8b-A examples/api, C8b-B boilerplate code, C8b-C deploy/docs + CheckTLS + health, OWASP_CHECKLIST.
3. After merge: gitleaks + trivy as required checks on master; release v0.9.0 tagged 2026-10-15 (61aa8e1).
4. Remaining advisories below (comments, ratelimit memory/proxy IP, heartbeat defer, CERBOS_PLAINTEXT strict bool, examples/api /audit any-user read).
5. The RDD stop hook proposes the whole branch vs master after every commit; it always fails lens_context_budget_exceeded (answer "Omitir esta vez").
Recreated on this machine from the Engram mirror odd/owasp-checklist-cycle2/tasks (the original file lived on the desktop PC).

## Objective
Close the remaining OWASP checklist failures in vogel (cycle 2) before releasing v0.9.0.

## Constraints
- vogel is the cornerstone library: tests + CHANGELOG + slice reviews; examples/ are usage samples only.
- Conventional commits, no AI attribution.

## Done
- [x] C1, C2, C2b, C3, C4, C4b, C5, C6 + slice reviews approved+acked (last review-6f839f4690f5a5f6 to 3cff281).
- [x] C7 re-audit found remaining FAILs -> C8.
- [x] C8a commit 1: c33db9b Cerbos CallTimeout (default 2s).
- [x] C8a commit 2: 6163840 WriteAccessError variant with resource/action.
- [x] Owner actions: private vulnerability reporting, master branch protection, tag ruleset on refs/tags/v*.

## Tasks
- [x] C8a-3: user_id reqctx slot filled by Authenticate, logged by StructuredLogger; UTC log timestamps. Redone from scratch (WIP stash stayed on the desktop PC). Route: delegated writer (2+ non-trivial files).
- [x] C8a-4: CI trivy (pinned + SHA256) image/config scan, Dependabot docker + docker-compose, golangci on boilerplate, CODEOWNERS go.mod/go.sum.
- [x] C8a-5 (54614a1, inline; root + boilerplate go build/go test ./... ok): bump google.golang.org/grpc 1.83.1 -> >=1.83.2 in root and examples/boilerplate go.mod (CVE-2026-84445 HIGH, found by the new trivy job; trivy.yaml stays red until done).
- [x] Review slice from 3cff281 to 54614a1: review-e47e6df489a45774 approved (4 lenses, high risk), acknowledged. Reviewed boundary -> 54614a1.
- [x] Ping deadline follow-up: 0d4f6c0 (inline, TDD RED->GREEN; go test -race ./authz/... ok; assess medium/under_budget, pending in next slice from 54614a1).
- [x] Review follow-ups done (inline; vet, go test -race ./httpx/... ./logger/..., golangci 0 issues): see last two commits. Were: SUGGESTION access.go:124-135 duplicated access-error body; SUGGESTION NewDefault UTC not proven by a test (CHANGELOG 179-185).
- [x] docs/WIRING.md: document the user slot / user_id (done in C8b-C, 5dd9d29).
- [x] C8b-A examples/api (a353b9d, a5d2219, 24bf135, 5c4ad28): list offset cap, title MaxLen, server timeouts, small per-IP rate limiter, role matrix test. Route: delegated writer.
- [x] C8b-B (3705acd, e079fca, 7488e46, 3dfe5d0, 3ef094a, c6c168a, e8c5fc1) boilerplate code: audit list offset cap 400, introspection/token lifetime/azp env config, SecurityHeaders first in chain, route+role tests, DB_REQUIRE_TLS independent of APP_ENV, IsProduction for swagger, invalid SWAGGER_ENABLED errors, health check cause-change logging + ignore caller cancel. Route: delegated writer.
- [x] C8b-C (1c51ceb, 3add0be, d8b45db, 2ebba53, 15589e8, 5dd9d29, 13b2744, 990f867) boilerplate deploy/docs: Dockerfile buildable in CI without the strip workaround, compose image pinning (developer compose :latest), cap_drop/no-new-privileges, worker HEALTHCHECK, patch cadence, THREAT_MODEL consumer duties, secret rotation without recreating postgres, docs/WIRING.md user slot. Route: delegated writer.
- [x] docs/security/OWASP_CHECKLIST.md (0c3f826, delegated writer; audited commit 82da551). 193 items in sections 1-18: PASS 79, PARTIAL 9, FAIL 0, CONSUMER 26, N/A 79; sections 19-20 N/A (no AI/LLM component; no listed stack sheet applies). Linked from THREAT_MODEL, SECURITY.md, README docs table; CHANGELOG Agregado. Verification: scripted check of 136 file/file:line evidence pointers (path exists, line <= file length): 0 missing (15 other backticked tokens are routes/MIME/import names, not paths); 29 cited Test* names all found as func in *_test.go; relative links in the 5 touched docs: 0 broken. Follow-ups listed in the doc's "Brechas conocidas" (CERBOS_PLAINTEXT lax bool, examples/api fake /audit read, proxy-IP limiter, 6 golangci in auth/zitadel/introspection*.go, gitleaks/trivy not required, plus PARTIALs 4.9, 11.2, 17.13, 10.8/18.5, 18.6/18.9).
- [x] Chained PRs #45..#61 merged (stacked-to-main, 2026-10-15); gitleaks+trivy required checks done 2026-10-15; release v0.9.0 tagged 2026-10-15 (61aa8e1).

## Evidence
- C8a-3 commit ac99460 (route: delegated writer). RED observed (reqctx build failure; then behavioral FAILs in httpx/middleware and logger) before GREEN. Slot installed in RequestContext; atomic.Pointer because http.TimeoutHandler can run Authenticate on a separate goroutine.
  - gofmt -l .: no output
  - go vet ./...: pass
  - go test -race ./reqctx/... ./httpx/... ./logger/...: ok (all 4 packages)
  - go test ./...: all ok
  - golangci-lint run ./...: 6 issues, all pre-existing in auth/zitadel/introspection*.go (identical on base); none in touched files
  - Follow-up: docs/WIRING.md does not yet mention the user slot / user_id (outside C8a-3 surfaces).
- C8a-4 commits 60bb990 (trivy.yaml), eab14f6 (golangci-lint on boilerplate), 058a6dd (dependabot docker/docker-compose, CODEOWNERS go.mod/go.sum, CHANGELOG). Route: delegated writer.
  - Trivy pinning: release binary v0.75.0 (v0.76.0 was published the same day; skipped), SHA-256 c6e65abd...b3f3f hardcoded; matched across trivy_0.75.0_checksums.txt, GitHub API asset digest and local sha256sum.
  - Image build: boilerplate go.mod has `replace => ../..`, so the Dockerfile cannot build in its own context; CI vendors and drops the single `RUN go mod download` line (asserted to match exactly once). Real fix belongs to C8b.
  - actionlint .github/workflows/*.yaml: clean. YAML safe_load of dependabot/ci/trivy: ok.
  - trivy config .: 0 misconfigurations (only the Dockerfile is detected; trivy has no compose scanner).
  - trivy image (api, worker, local build): alpine 3.23.6 0 vulns; gobinary 1 HIGH, google.golang.org/grpc v1.83.1 CVE-2026-84445 (fixed 1.83.2) -> C8a-5.
  - examples/boilerplate: go build, go vet ok; golangci-lint --config ../../.golangci.yml --build-tags=integration: 0 issues. No Go touched, root go test not rerun.
- C8b-A commits (route: delegated writer). RED observed before GREEN for items 1-4; role matrix green on first run (pins behaviour), mutation-checked.
  - a353b9d fix: GET /documents offset > workflow.MaxListOffset -> 400 (same as Inbox); title MaxLen 200 runes via request.Validator. RED: title case 200 != 400; List test panicked reaching nil store.
  - a5d2219 feat: newAPIServer + HTTP_{READ_HEADER,READ,WRITE,IDLE}_TIMEOUT (5s/15s/30s/60s, must be > 0), MaxHeaderBytes 64 KiB. RED: build failure (undefined Config fields/newAPIServer).
  - 24bf135 feat: ipRateLimiter (fixed window, per process, keyed on reqctx IP), mounted on /api/v1 before Authenticate, 429 RATE_LIMITED + Retry-After; RATE_LIMIT_REQUESTS=300 / RATE_LIMIT_WINDOW=1m. golang.org/x/time not a direct dep, so no token bucket. RED: build failure; router wiring test mutation-checked (removing the mount fails it).
  - 5c4ad28 test: rolematrix_test.go, 14 route rows x 6 callers, plus chi.Walk coverage check. Mutation (fakeChecker allows every action) fails 12 cells.
  - gofmt -l examples/api: no output; go vet ./examples/api/... (and -tags integration): ok; go test -race ./examples/api/...: ok; golangci-lint run ./examples/api/...: 0 issues; go build ./...: ok.
  - Open: env.example (the real file; surface named .env.example) lacks the new HTTP_*_TIMEOUT / RATE_LIMIT_* vars; CHANGELOG has examples/api precedent but was outside surfaces; fakeChecker lets any authenticated caller read /audit (pinned, not changed); README still says "No tiene tests" (stale, pre-existing).
- C8b-B commits (route: delegated writer). RED observed before GREEN for items 1, 3, 5-8; role matrix green on first run (pins behaviour), mutation-checked.
  - 3705acd docs(audit/httpx): FiltersFromRequest doc states the offset cap lives in audit.Filters.Page (repo) and Handler.List maps ErrOffsetOutOfRange to 400; a custom handler must map it. Library already enforced the cap (audit.MaxListOffset + Messages.InvalidOffset), so no library behaviour change; adding a parse-time check would need an English-only message (request.Messages has no "too large" field; request/ is outside surfaces).
  - e079fca fix: boilerplate AuditHandler.List mapped ErrOffsetOutOfRange to 500 -> now 400 VALIDATION_ERROR field=offset (Spanish copy). RED: status 500 != 400. Stub repo now calls f.Page() like audit/postgres.
  - 7488e46 feat: DB_REQUIRE_TLS (default true in every APP_ENV; deploy compose api/worker and README .env sample set false); strict parseBool (SWAGGER_ENABLED, DB_REQUIRE_TLS, ZITADEL_INTROSPECTION fail boot on unparsable); Swagger-in-production refusal moved to Config.validate using IsProduction; ZITADEL_HTTP_TIMEOUT (10s, >0), ZITADEL_MAX_TOKEN_LIFETIME (12h, 0 disables, <0 error), ZITADEL_ALLOWED_AUTHORIZED_PARTIES (empty = no azp check, per auth/zitadel docs), ZITADEL_INTROSPECTION (+KEY_FILE xor CLIENT_SECRET, CACHE_TTL, CACHE_MAX_ENTRIES; credential without the switch is an error). Documented in README, deploy/env.example, compose comments. RED: build failure (parseSwaggerEnabled signature, defaultMaxTokenLifetime). Mutations: DB default derived from APP_ENV, IsProduction check removed, lax bool parsing -> each fails named tests.
  - 3dfe5d0 fix: vmw.SecurityHeaders is the router's first middleware (was inside /v1 after Recovery/limiter/Timeout). RED: 404 outside /v1 and 429 from global limiter lacked headers (401 already had them).
  - 3ef094a test: rolematrix_test.go, 6 routes (incl. swagger, registered on) x 5 callers against a stand-in asymmetric policy (auditor view-only, admin list+view) + chi.Walk coverage. Mutations: list->view on /v1/audit/ fails the auditor cell; removing the /v1/readyz row fails coverage.
  - c6c168a fix: health ping logs once per distinct error text (and recovery at Info); a ping failing while the caller ctx is done returns errCallerGone: no log, no state change, nothing written. RED: changed cause not logged; canceled caller logged ERROR.
  - e8c5fc1 docs(changelog): Agregado (boilerplate hardening) + Cambiado (DB_REQUIRE_TLS behaviour change).
  - gofmt -l .: no output
  - root go vet ./... && go test -race ./...: ok
  - examples/boilerplate go vet ./... (also -tags integration), go test -race ./...: ok; golangci-lint --config ../../.golangci.yml ./... (also --build-tags=integration): 0 issues
  - root golangci-lint run ./audit/...: 0 issues (health/ does not exist; health lives in the boilerplate handler)
  - Open: deploy/RUNBOOK.md section 0 / table still says APP_ENV decides RequireTLS (outside C8b-B surfaces) -> C8b-C. docs/swagger.* not regenerated (they already lack the audit params). Not done: CERBOS_PLAINTEXT still uses lax config.Bool.
- C8b-C commits (route: delegated writer). RED observed for the worker heartbeat (build failure: undefined checkHeartbeat/runHeartbeat/cmdHealthcheck) before GREEN.
  - 1c51ceb build: Dockerfile builds with the repo root as context (-f examples/boilerplate/Dockerfile .), copies root + boilerplate go.mod/go.sum before go mod download, -trimpath, ARG APP_DIR (default examples/boilerplate; APP_DIR=. for a copy without replace, not exercised). Root .dockerignore replaces examples/boilerplate/.dockerignore (deleted; it no longer applies). Deploy compose builds via x-build anchor (context ../../.., dockerfile examples/boilerplate/Dockerfile). README build instructions updated.
  - 3add0be ci: trivy.yaml builds the real Dockerfile from the root; strip workaround and setup-go removed.
  - d8b45db feat: worker heartbeat file (os.TempDir()/vogel-worker.heartbeat, every 10s after queue.Start, removed on shutdown) + `worker healthcheck` subcommand (fails if missing/unparsable/older than 30s); Dockerfile worker HEALTHCHECK CMD ["./worker","healthcheck"]. 990f867 gosec G304 nolint (fixed path).
  - 2ebba53 chore: postgres:17.11-alpine3.24@sha256:b0f9560a... (same digest 17-alpine already had; resolved via Docker Hub tag API) in deploy, developer and examples/api compose; ghcr.io/cerbos/cerbos:0.56.0@sha256:540643bc... (docker buildx imagetools inspect). MinIO removed from developer compose: minio/minio no longer exists on Docker Hub (pull denied, hub API object not found), so it could not be pulled or pinned. x-hardened adds cap_drop ALL + no-new-privileges (migrate, grants, api, worker); postgres cap_drop ALL + cap_add CHOWN,DAC_OVERRIDE,FOWNER,SETGID,SETUID + no-new-privileges; developer postgres nnp, cerbos cap_drop ALL + nnp. Dependabot docker-compose regex (dependabot-core docker_compose/file_fetcher.rb: (docker-)?compose(-[\w]+)?(?>\.[\w-]+)?\.ya?ml) covers docker-compose.developer.yaml; comment added in dependabot.yml.
  - 15589e8 docs: RUNBOOK section 0 DB_REQUIRE_TLS (not APP_ENV), optional settings table (DB_REQUIRE_TLS, ZITADEL_*), hardening + worker healthcheck, new "Parches" subsection (weekly Dependabot/Trivy, trivy failure procedure); section 10 role password rotation via \password + up -d --no-deps api worker.
  - 5dd9d29 docs: THREAT_MODEL section 6 consumer-duty table + checklist; WIRING section 1 user slot subsection, RequestContext snippet synced with code.
  - 13b2744 docs(changelog): Agregado (deploy hardening) + Cambiado (root build context, MinIO removed).
  - docker build (root context) api + worker: ok (api 95.9MB, worker 72.1MB). `docker run boilerplate:worker healthcheck` without heartbeat: exit 1.
  - docker compose config: deploy (scratch copy with env), developer, examples/api: ok.
  - docker compose up developer: postgres + cerbos healthy. Deploy stack (scratch copy, local images): postgres healthy with cap set, migrate exit 0, grants exit 0, worker healthy (heartbeat under read_only + cap_drop). api exited at OIDC discovery (no Zitadel available locally) after DB connect and metrics listener start: not caused by hardening, but api healthy-start not proven.
  - actionlint .github/workflows/*.yaml: clean. dependabot.yml yaml.safe_load: ok.
  - trivy v0.75.0 (SHA-256 verified): config . 0 misconfigurations; image api/worker HIGH,CRITICAL --ignore-unfixed: 0 (alpine 3.23.6 + gobinary).
  - examples/boilerplate: go vet ./... ok; go test -race ./... ok; golangci-lint --config ../../.golangci.yml ./... (and --build-tags=integration): 0 issues; gofmt -l: none.
  - Open: examples/boilerplate/Makefile docker-build still builds with the boilerplate dir as context (outside surfaces; was already broken by the replace). README copy-out step 5 mentions .dockerignore as a reference dotfile; the reference now lives at the repo root.

## Review slice 54614a1..708175e (2026-10-14)
- review-5a322d0907bb77bd: correction_required on R3-db-require-tls-migrate (CRITICAL, inferential; verified false as stated since migrate used DB_URL directly, but user chose the real hardening). Correction 708175e: postgres.CheckTLS + api migrate honors DB_REQUIRE_TLS, compose override on migrate, RUNBOOK. Verified: go test -race ./postgres, boilerplate vet/test -race/golangci 0 issues, compose config ok.
- Recovered as review-97af77d979019645 -> approved + acknowledged. Reviewed boundary -> 708175e.
- [x] CHANGELOG entry for postgres.CheckTLS: 4a1ffce (also carries the health CHANGELOG wording).
- [x] Health hung-DB fix: 82da551 (TDD RED 200/no log -> GREEN; only context.Canceled is caller-gone; compose TLS comment scope fixed). boilerplate vet/test -race/golangci 0 issues.
- [x] Advisory follow-ups (non-blocking): verified 2026-10-20. Health cancel handling (82da551 + TestHealth_CanceledCallerOnAHungDatabaseIsStillLogged), proxy note in examples/api/ratelimit.go:27-29, compose comment and test comment fixed. Left open by choice: ratelimit memory bound, health cause dedupe, worker heartbeat defer (SUGGESTION).
  - WARNING x2 (R3+R4) health_handler.go:144-146: caller deadline/cancel can mask a hung DB (treat only context.Canceled as caller-gone, or probe with own deadline detached from caller).
  - WARNING examples/api/router.go:86: rate limiter keys on proxy IP behind a proxy (documented; consider note/RealIP guidance).
  - WARNING examples/api/handler_test.go:67-70 misleading title test comment.
  - WARNING deploy/docker-compose.yaml:27-28 TLS override scope comment (now also migrate).
  - SUGGESTION ratelimit.go:87-99 memory bound; health cause-string dedupe; worker heartbeat defer does not wait.

## Review slice 708175e..0c3f826 (2026-10-14)
- review-5f4b12eceeab2671 approved + acknowledged. Reviewed boundary -> 0c3f826.
- Advisories: CHANGELOG CheckTLS "unbacked" and compose comment "without setting" are slice-boundary false positives (CheckTLS at postgres/postgres.go:210, migrate override present, both in 708175e = base). Checklist line anchors may drift (SUGGESTION).
- [x] (b351840: ping detached via context.WithoutCancel + own timeout, errCallerGone removed; TDD RED [] -> GREEN; boilerplate vet/test -race/golangci 0 issues; assess medium/under_budget) R4 WARNING health_handler.go:139-144: kubelet probe timeout closes the connection -> server sees context.Canceled, so a hung DB under kubelet probes is still not logged. Candidate fix: run the ping detached from caller cancellation (context.WithoutCancel + own timeout) and drop errCallerGone.

## Delivery (2026-10-15)
- User chose chain strategy `stacked-to-main` ("dale"). 17 slices as contiguous commit ranges (history not rewritten), branches owasp-c2/NN-<slug>, PRs #45..#61, each based on the previous branch; #45 targets master.
- size:exception on #46 #48 #50 #51 #53 #56 #57 #58 #61 (single commits or units that would split).
- CI only triggers pull_request for main/master: each PR gets CI after the previous one merges and it is retargeted (`gh pr edit N --base master`). Merge with a merge commit only (rebase/squash rewrite SHAs and pollute the next diff). #45 merged as da44c4d, #46 retargeted to master. deleteBranchOnMerge=false.
- b351840 (in #61) still has no native review.
- Next: merge #45 when CI is green, retarget #46, repeat; then gitleaks+trivy required checks, release v0.9.0.
- 2026-10-15 user: "Ok" (authorized rewriting + force-push of owasp-c2/02..17).
- Root golangci-lint failed at every slice end since 8b846e3 (never run: branch had no PR to master). Rebuilt chain: 4769a1f fix(auth/zitadel) checked cache type assertions (entryOf) + test wastedassign, after 8b846e3; 048e1ef fix(auth/zitadel) checked singleflight result + annotated nolint:errorlint (%v matches SDK oauth/authorizer.go:98) at end of slice 03; 990f867 gosec annotation moved into slice 15. Tree vs b351840 differs only in introspection.go/_test.go (+20/-6). Local sweep (lint with CI flags, build, test -race, root + boilerplate) OK at all 16 slice ends. Force-pushed with leases; PR bodies regenerated. New tip 3babf07. security/owasp-checklist-cycle2 left at b351840 (not rewritten).
- Retarget needs close+reopen to trigger CI ('edited' fires no workflow).
- 2026-10-15 10:42: chain complete, #45..#61 all merged with merge commits (last 8a00771). origin/master tree == rebuilt tip 3babf07. No check failures after the rebuild. Dependabot opened #62..#67 (new configs from slices 07/10).
- Pending: delete owasp-c2/* and security/owasp-checklist-cycle2 (ask), gitleaks+trivy required checks, triage dependabot #62-#67, release v0.9.0, native review of the detached health ping commit.
- 2026-10-15 RDD: whole old branch (8b846e3..b351840, 6337 lines) -> lens_context_budget_exceeded (user granted; nothing created). Reduced candidates: 4769a1f vs 8b846e3 review-12adb9c58d5e7d1d (risk+reliability) approved+acked, advisory R3-001 entryOf panic branch untested; 048e1ef vs parent review-afd8fcff733b6c85 (risk+resilience+reliability) approved+acked, advisory: guard `|| res == nil` for typed-nil shared result (introspection.go:198-202, not a regression); master vs 3babf07^ (detached ping, 97 lines) assess medium/under_budget, review not due.
- Local checkout now on master 8a00771.
- 2026-10-15: deleted 18 remote branches (owasp-c2/* + security/owasp-checklist-cycle2) with user OK. Local security/owasp-checklist-cycle2 still exists (stale, b351840).
- 2026-10-15: local security/owasp-checklist-cycle2 deleted with user OK.
- 2026-10-15: gitleaks added to master required checks. #68 (trivy pull_request without paths-ignore, so a required check always reports) reviewed (review-5ff2b133dc7b81d3, 4 lenses, approved+acked; advisories: retry for DB/image fetch, or a cheap same-name fast path for docs-only PRs) and merged as 37e033a; "Trivy image + config" added to required checks. Required now: Lint + Unit + Build, govulncheck, Boilerplate example, gitleaks, Trivy image + config.
- 2026-10-15 Dependabot triage (user: "dale con ese plan"): #62/#66/#63 rebased and green but BLOCKED: master requires code-owner review (require_code_owner_reviews=true, CODEOWNERS from slice 10) -> needs the user's approval. #67 (postgres 17->18) closed; #70 adds a Dependabot ignore for postgres semver-major. #64 closed, superseded by #69: the bump + fix(decimalx) exponentSuffixOutOfReach pre-check in Parse (decimal v1.5.0 MaxDecodeExponent turned 1e100000000 into ErrNotParseable) + fixtures via decimal.New + boilerplate tidy + CHANGELOG. #69 escalated to high (item 2, anti-DoS guard), review-0efb6578746f8005 approved+acked; follow-ups applied in 59cdf47. #63 and #69 both touch examples/boilerplate/go.mod: merge #69 first, then let Dependabot rebase or close #63.
- 2026-10-15 user approved #62/#66/#63 as code owner. Merged #62 e944742, #66 1a14983, #69 d3a3844, #70 3b83748. #63 conflicted after #69 (left: httprate 0.16.1, prometheus client_golang 1.25.0); asked "@dependabot recreate" - needs a fresh code-owner approval (dismiss_stale_reviews=true).
- 2026-10-15: Dependabot declined to recreate #63 and opened #71 (same 2 bumps); I opened #72 without noticing #71. #72 reviewed (review-a1d02d203651033d, reliability, approved+acked), merged 37485af; #63/#71 closed. No open PRs. Remaining: release v0.9.0; optional res==nil guard, trivy retry/fast path.
- 2026-10-15: release prep #73 (review-3fc11a696f75e054 approved+acked) merged 61aa8e1; user OK -> annotated tag v0.9.0 on 61aa8e1 pushed; release.yaml (SBOMs) running.
- 2026-10-15: release.yaml success; https://github.com/Kafeiih/vogel/releases/tag/v0.9.0 has SHA256SUMS, vogel.cdx.json, vogel-boilerplate.cdx.json. Cycle 2 closed. Open optional follow-ups: res==nil guard (introspection.go shared result), entryOf panic-branch test, trivy retry / docs fast path, deliberate Postgres 18 migration.
- 2026-10-15: optional follow-ups 1+2 done: #74 (sharedResult typed-nil guard + entryOf panic test; review-8df68cdb65ffc012 risk+reliability approved+acked) merged. Remaining optional: trivy retry/docs fast path, deliberate Postgres 18 migration.
