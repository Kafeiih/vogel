Objective: close #147 (OWASP 17.13, PARTIAL): generic idempotency keys for mutations, in two layers — a transactional core that runs inside the mutation's own transaction (exactly once) and a generic HTTP middleware for routes that do not adopt it.
Branch: feat/idempotency-keys from master `ef23792`. Delivery: stacked-to-main (cached from earlier features): PR-A T1, PR-B T2-T3. Forecast ~1,100 authored lines. Test runner: `go test -race ./idempotency/...`, `go test -tags=integration -race ./idempotency/...`, `cd examples/boilerplate && go test ./...`, `cmd/create-vogel-app/check-generated.sh`, golangci-lint v2.13.2.

## Specs

- **S1 Source.** #147: "un middleware o helper `Idempotency-Key` con almacenamiento en Postgres y TTL, más tests de reintento y de concurrencia. Después, actualizar el checklist." Checklist row 17.13: "no ofrece claves de idempotencia genéricas para mutaciones".
- **S2 Storage.** New packages `idempotency` (core types, errors), `idempotency/postgres` (store), `idempotency/migrations` (own version table `idempotency_db_version`, like `audit`, `workflow`, `action`, `document`). Table `idempotency_key`: primary key `(scope, key)`, a request fingerprint, status `in_progress`/`completed`, the stored response (status, headers, body), a lease (`locked_until`) and `expires_at`. Default TTL 24h. `DeleteExpired` removes expired rows in bounded batches; scheduling it is the consumer's job.
- **S3 Transactional core** (L2: "Ambas capas"). `idempotency.Do` runs inside the caller's transaction: it claims `(scope, key)` in that transaction, runs the mutation, and stores its result in the same transaction, so the key and the mutation commit or roll back together (exactly once). A concurrent duplicate waits on the key until the first transaction ends: it replays the stored result after a commit, and runs the mutation itself after a rollback. A key reused with a different fingerprint fails with `ErrKeyReused`. An expired key counts as new.
- **S4 HTTP middleware.** Header `Idempotency-Key`. Scope = authenticated principal + method + route. Fingerprint = hash of method, path and body (the body is read bounded and restored for the handler).
  - First request: reserve the key with a lease, run the handler, store the response (status < 500), replay it on later requests with `Idempotent-Replayed: true`.
  - Duplicate while the first is still running (lease alive): 409 `CONFLICT`.
  - Same key, different fingerprint: 422 `UNPROCESSABLE_ENTITY`.
  - Handler answers 5xx or panics: the reservation is released, so a retry runs again.
  - Lease expired without a stored response (the process died): the next request takes it over and runs. This is the middleware's documented gap; routes that need exactly once use S3.
  - Header optional by default; an option makes it required on a route (400 `VALIDATION_ERROR` when missing). A request without a principal passes through untouched.
  - `Set-Cookie` is never stored or replayed.
- **S5 Docs.** README rows for the three packages, CHANGELOG `Agregado`, OWASP checklist 17.13 to PASS with evidence (and its open-findings row 8 closed), migration guide section for `idempotency/migrations`.

Assumptions (to confirm in review): response bodies over a configurable cap (default 1 MiB) are not stored — the reservation is released and logged, so such routes behave as if the header were absent; boilerplate wiring is out of scope for this feature (follow-up issue).

## Tasks

| ID | Specs | Route | Status | Evidence |
|----|-------|-------|--------|----------|
| T1 | S2 S3 | inline | done | `44d7d83`; RED: stub store (no claim) failed replay, reuse and expiry tests |
| T2 | S4 | inline | done | `9c6acb0`; RED: compile (no package); mutations of Set-Cookie stripping, 5xx release and reservation-scoped Release each fail a test |
| T3 | S5 | inline | done | `67b1163` |

Route evidence: T2 depends on T1's store; no parallel units. Risk: item 2 (security control) and item 4 (concurrency on the key row) — independent verifier at the end.

## Log

- L1 2026-11-01 Oscar: "dale con el #147"
- L2 2026-11-01 Oscar, asked where the key lives relative to the mutation's transaction, chose: "Ambas capas (Recommended)" — transactional core (`idempotency.Do` inside the service's tx, exactly once) plus a generic HTTP middleware for the rest.
- L3 2026-11-01 Defaults stated to Oscar before the question and not objected: header `Idempotency-Key`; scope user + method + route; body hash, 422 on mismatch, 409 while in progress, replay with `Idempotent-Replayed: true`; own Postgres table and migrations; 24h TTL with a cleanup function the consumer schedules; header optional with an option to require it.
- L4 2026-11-01 Scope uses the request path (`r.URL.Path`), not a route pattern: the concrete path keeps `/cases/1/claim` and `/cases/2/claim` apart without a router dependency. The fingerprint also covers the raw query.
- L5 2026-11-01 Added a `reservation` token column (unreleased migration 001 edited in place): without it, a slow request whose lease was taken over could store or release the new holder's reservation, since both share the same fingerprint.
- L6 2026-11-01 The first concurrency test hung on a failing assertion: the open transaction kept a pool connection and `pool.Close` waited forever. The test now rolls it back in a cleanup registered after the pool's.
- L7 2026-11-01 Verification: `go test -race ./...` OK except the 4 Windows-only `internal/scaffold` failures (#202); `go test -tags=integration -race ./idempotency/...` OK; boilerplate OK; `check-generated.sh` OK; golangci-lint 0 issues. Next: independent verifier (items 2, 4).
- L8 2026-11-02 Independent verifier: S1-S3 and S5 pass, no security hole; S3 held under 20 concurrent transactions (commit and rollback), mixed fingerprints and concurrent reclaim. Two low-impact S4 defects fixed with RED first: (1) outer-middleware headers such as X-Request-ID were stored and replayed over the retry's own — now only headers the handler set or changed are stored; (2) a duplicate racing a Release got 503 (11 of 100 in the RED run) — Reserve now claims again when the conflicting row vanished. Advisories fixed: `Scope` is now `http:` + a length-prefixed hash (no collision through spaces, bounded length, own namespace apart from Store.Do), raw `set-cookie` keys are stripped, the recorder implements http.Flusher. Left as advisories: Do on a lapsed middleware lease waits for the TTL; `now()` is transaction start time inside long transactions.
