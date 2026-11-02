Objective: close the last two event-format follow-ups of #184 and #185: cover `token_id`/`scope_id` with the hash chain, and record the deadline a token enters a node with so the history can rebuild it (rule-resolved deadlines included). `vogel/workflow` is greenfield, so the v2 digest replaces v1 instead of living beside it.
Branch: feat/workflow-event-format-v2 from master `8d4d77e`. Delivery: ask-on-risk (forecast ~350 authored lines, one PR). Test runner: `go test -race ./workflow/...`, `go test -tags=integration -race ./workflow/...`, `cd examples/boilerplate && go test ./...`, `cmd/create-vogel-app/check-generated.sh`, golangci-lint v2.13.2.

## Specs

- **S1 Digest v2.** From #184: "`workflow_event.token_id`/`scope_id` no entran en el hash chain de la 007. Cubrirlos necesita una etiqueta de versión nueva del digest (`vogel.workflow_event.v2`) y que `VerifyChain` distinga v1 de v2."
  - Migration 010 replaces `vogel_workflow_event_digest` with the v2 encoding: tag `vogel.workflow_event.v2`, every v1 field in the same order, then `token_id`, `scope_id` and `deadline_at` (S2), NULL as length -1.
  - Per L2, `VerifyChain` does not distinguish v1 from v2: there are no v1 chains to keep verifiable. The migration header says that a database with v1 digests would report them as broken.
  - `VerifyChain` reports `ErrChainBroken` when a stored `token_id`, `scope_id` or `deadline_at` no longer matches its digest.
  - Down restores the v1 function and drops the column.
- **S2 Entered deadline in the event.** From #185: "Un plazo resuelto por `DeadlineRule` no queda en ningún evento: reproducir la historia no lo reconstruye, y el invariante de replay de los tests sólo cubre `Node.Deadline` (ningún test con reglas puede usar `assertInvariants`). Considerar registrar el plazo con el que entró el token en `opened`/`moved`."
  - New nullable column `workflow_event.deadline_at` (migration 010) and field `Event.DeadlineAt *time.Time`.
  - An `opened` or `moved` event whose token comes to rest at a task node records the deadline the token got there (`Node.Deadline` or `Node.DeadlineRule`), or nil when the node declares none. Every other event, and a hop that does not come to rest (decision, split, join, terminal), records nil.
  - `CurrentEventSchema` becomes 4; an older event reads with `DeadlineAt` nil.
- **S3 Replay.** `assertInvariants` rebuilds every token's deadline from the events (`Event.DeadlineAt` on entry, `deadline_changed` afterwards) instead of from `Node.Deadline`, and a test with a `DeadlineRule` runs it.
- **S4 Docs.** Migration 010 header, CHANGELOG `[Unreleased]`, README rows for `workflow` and `workflow/postgres`.

## Tasks

| ID | Specs | Route | Status | Evidence |
|----|-------|-------|--------|----------|
| T1 | S2 S3 (Go side: field, emission, replay) | inline | done | `aaf9508`; RED: replay from events failed 5 deadline tests; mutation of the route emission fails `TestEvents_RecordTheDeadlineTheTokenEnteredWith` |
| T2 | S1 S2 (migration 010, repository column, digest v2, schema 4) S4 | inline | done | `770006c`; RED: tamper of token_id/scope_id undetected, deadline_at missing, 010 absent |

Route evidence: one behavior per task, each under the writer threshold; no parallel units (T2 depends on T1's field). Risk: item 1 (persisted format, digest function) — independent verifier after T2.

## Log

- L1 2026-11-01 Oscar: "Primero mergea en orden, después seguimos con #184"
- L2 2026-11-01 Oscar, asked whether any database holds v1-chained events that `VerifyChain` must keep validating: "no hay, es greenfield"
- L3 2026-11-01 Design: the deadline goes in a new column rather than `Event.Detail`, because a `moved` Detail already carries the expiry cause (`workflow/operations.go:217-226`) and a column enters the digest directly.
- L4 2026-11-01 `CurrentEventSchema` 4 moved from T1 to T2: it is bumped in the commit that also persists the field, so no commit announces a format the database cannot store.
- L5 2026-11-01 `TestMigration009_UpDownRoundTrip` now writes its data at 010 and rolls 010 back before 009's Down: the engine of this version needs `deadline_at` (010's documented deploy order).
- L6 2026-11-01 Verification: `go test -race ./...` OK except the 4 `internal/scaffold` tests that also fail on master on this Windows host; `go test -tags=integration -race ./workflow/... ./action/... ./document/...` OK; boilerplate `go test -tags integration ./...` OK; `check-generated.sh` OK; golangci-lint v2.13.2 0 issues on both modules. Next: independent verifier (risk item 1).
