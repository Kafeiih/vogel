Objective: close the gaps of the 2026-10-21 BPMS audit (items 1-4) and serve the boilerplate API from / instead of /v1.
Branch: fix/boilerplate-bpms-gaps (from master 79663e5). Delivery: ask-on-risk. Test runner: go test, go test -tags integration where applicable, cmd/create-vogel-app/check-generated.sh, docker compose config.

## Specs

- S1 "arreglá del 1 al 4", item 1, the seven review follow-ups:
  - S1a docflow cancels renderer errors a retry cannot fix (Gotenberg 4xx other than 408/429, an oversized PDF, an empty PDF) instead of retrying until attempts run out; 5xx, 408, 429 and transport errors still retry.
  - S1b an effect missing from the Registry is retried (deploy skew: an old worker may pick up a new effect) instead of cancelled.
  - S1c the best-effort Delete after a failed insert is bounded by a timeout and logged with the context.
  - S1d GOTENBERG_TIMEOUT has an upper bound that keeps the render inside River's 1m job timeout.
  - S1e NotifyHandler no longer resends the messages that already went out when a later send fails (decision: one job per message).
  - S1f deploy/env.example has no placeholder DB_HOST that compose would accept.
  - S1g migrate's optional depends_on on postgres: verify; it is intended (postgres lives in the bundled-db profile, RDS hosts do not start it).
- S2 item 2, docs: BPMS.md does not promise an orphan sweeper that does not exist; README does not contradict itself when bpms is on.
- S3 item 3: deploy/env.example has one STORAGE_CONFIGS line, so uncommenting one storage cannot be silently overridden by another.
- S4 item 4: boilerplate BPMS files follow the comment policy "elimina los comentarios de codigo, todos. solo cuando sea urgente/critico/extramentamente necesario y solo 1 linea" (boilerplate only; library godoc stays).
- S5 "Route esta inicializado en /v1 lo cual es incorrecto es una modalidad que se quito para que el nginx renombre 1 app como una version especificar ahora trabajamos directo con /" and "Ojo me refiero al archivo router.go donde se configura el Route() en todos los examples": every example's router.go drops its version prefix (boilerplate /v1, examples/api /api/v1); the boilerplate mounts its routes at / (healthz, readyz, livez, swagger, audit), with Swagger BasePath, Dockerfile healthcheck, Prometheus alerts, docs and tests following.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S1a | inline | done | 7a493cd |
| T2 | S1b | inline | done | 7a493cd |
| T3 | S1c | inline | done | 7a493cd |
| T4 | S1d | inline | done | GREEN, see Log |
| T5 | S1e | inline | done | see Log |
| T6 | S1f S1g S3 | inline | done | GREEN, see Log |
| T7 | S2 | inline | done | GREEN, see Log |
| T8 | S4 | inline | done | GREEN, see Log |
| T9 | S5 | inline | done | GREEN, see Log |

Route evidence: no Writer trigger (units depend on each other's docs/CHANGELOG, each under ~100 lines); parent writes inline.

## Log

- L1 2026-10-21 user: "dale, arreglá del 1 al 4 - lo otro que me hace ruido es que Route esta inicializado en /v1 lo cual es incorrecto es una modalidad que se quito para que el nginx renombre 1 app como una version especificar ahora trabajamos directo con /"
- L2 the audit's 1-4: (1) the seven review follow-ups, (2) BPMS.md sweeper + README contradiction, (3) duplicate STORAGE_CONFIGS in deploy/env.example, (4) comment policy in the boilerplate BPMS files.
- L3 S1g: required: false is intended, postgres is in profile bundled-db; no change.
- L4 S1d: River v0.49.0 JobTimeoutDefault = 1m and worker/ sets no JobTimeout; max GOTENBERG_TIMEOUT 45s leaves room for upload and insert.
- L5 2026-10-21 user: "Ojo me refiero al archivo router.go donde se configura el Route() en todos los examples" -> S5 covers examples/api (/api/v1) too.
- L6 T1-T3 RED observed (undefined ErrRenderRejected/CleanupTimeout, unknown effect cancelled), GREEN go test ./action ./document/...; committed 7a493cd.
- L7 T4 RED "timeout past the job timeout" then GREEN; T6 compose config rejects empty DB_HOST ("set DB_HOST in deploy/.env") and renders with a host; T9 promtool test rules SUCCESS, root and boilerplate go test ./... green. Local golangci-lint is too old for go1.26 (typecheck export data v5): CI lints. swag init regenerated ~900 unrelated lines: reverted, only basePath edited by hand.
- L8 T5 needs a decision: Message has no ID, so stopping resends means either one job per message or a per-message idempotency key.
- L9 native review review-44a75a8e521fd53e (resilience, readability, reliability) approved and acknowledged on b306ba3. Advisories applied: 401/404 retried (rejected list 400/403/409/413/422), stale README rows (root docflow/gotenberg, examples/api guard test). Not applied: unknown-effect retry noise (requested trade-off), bpms nested in storage block (bpms requires storage in .vogel/template.json), removed bpms comments (user's comment policy).
- L10 2026-10-21 user chose "Un job por mensaje (Recomendado)" for S1e. Args.Part/Run.Part (unique); Worker passes an unexported enqueue-parts func on ctx; NotifyHandler with >1 message enqueues parts and sends nothing, each part re-runs build and sends its own message; part past the built messages cancels. RED: unknown field Part; GREEN go test ./action and -tags integration ./action ./document/...
- L11 2026-10-21 user: "si" to a follow-up PR with the review advisories (403/413, 1-based part, rolling deploy, RUNBOOK). Branch fix/bpms-review-followups stacked on fix/boilerplate-bpms-gaps. 403/413 now retried (rejected: 400/409/422 + oversized); notify errors number messages from 1; RUNBOOK section 13 explains cancelling stranded vogel_action jobs (SQL mirrors River's JobCancel for non-running jobs). Rolling-deploy N*N: no code change, no released vogel has action (v0.13.2 has none), so no old worker can run a part job without Part.
- L12 Trivy failed on #132 from CVE-2026-111943 published 2026-10-21 (not caused by this feature); fixed separately in #133 (golang 1.27.3 builder, Cerbos ignore entry).
