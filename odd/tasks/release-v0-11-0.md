Objective: close #103, #100 and #105, then release v0.11.0; #101 (OpenTelemetry) deferred to v0.12.0.
Branch: one branch + PR per issue from master. Delivery: single-pr per issue. Test runner: go test -race ./... (+ promtool for alerts).

## Specs
- S1 (#103): "Add the missing failure-path tests and decide on configurable buckets." — panic log line and its request fields; 5xx counted with its route pattern; unmatched route collapsing to "unknown".
- S2 (#100): "Serve `/metrics` from `examples/boilerplate/cmd/worker` on its own listen address, configured and bound like the api's metrics listener, and register `worker.WithMetrics` on it." + "Add worker rules to `deploy/prometheus/alerts.yml` with tests in `alerts_test.yml`" + "Mention the endpoint in the RUNBOOK and README."
- S3 (#105): "Triage each one: fix, document further, or close as accepted." — includes the follow-up comments from #106 and #107; priority: PostgreSQL error detail in the slow-query WARN.
- S4: release v0.11.0 after S1-S3 (same procedure as v0.10.1).

## Tasks
- [x] T1 S1 inline — PR #108 (d8a3925 + prealloc fix); found and fixed: panics behind Recovery were not counted by Metrics
- [x] T2 S2 inline — PR #109 (63711ee); promtool + check-generated.sh OK
- [x] T3 S3 inline — PR #110 (fix/advisories-105, 8 commits); verifier no blockers; review approved, warnings fixed in 8af7cc2
- [x] T4 S4 inline — PR #111 merged (bb1eae8), tag v0.11.0 pushed, release.yaml success, assets SHA256SUMS + 2 SBOMs

## Log
- L1 (2026-10-19): "cerremos todos los procesos pendientes y ahi sacamos release"
- L2 (2026-10-19): user agreed ("dale") to defer #101 to v0.12.0.
- L3 (2026-10-19): #105 triage plan — fix: boilerplate CI template promtool, zitadel comment wrap, fresh-slice test, pg error in slow-query WARN under hash mode, named scalar panic values, filter param constants, sql_hash length constant, Truncate backoff cap. Accept/close: CI cancel gating, presigned orphans, DBFromContext fallback, build() wiring test (pin equals SDK default), keyed hash.
- L4 (2026-10-19): PRs #108, #109, #110 all CI green; awaiting merge approval, then release v0.11.0 (same procedure as v0.10.1: release branch bumping CHANGELOG, README, MIGRATION_GUIDE, boilerplate README and go.mod; annotated tag "vogel v0.11.0" triggers release.yaml).
- L5 (2026-10-19): #108, #109, #110, #111 merged; v0.11.0 released: https://github.com/Kafeiih/vogel/releases/tag/v0.11.0. Only #101 remains open (v0.12.0).
