Objective: extract the reusable OpenAPI contract-first glue proven in go-safo (PR #178) and complex into a vogel package, and migrate the boilerplate from swaggo to an OpenAPI 3.1 contract with an oapi-codegen strict server.
Branch: feat/openapi-contract (from master 79593b7). Delivery: exception-ok (single PR, one commit per task; chosen 2026-10-27). Test runner: go test ./... (library and boilerplate), cmd/create-vogel-app/check-generated.sh, make generate + git diff, redocly lint.

## Specs

- S1 "armemos un paquete de esto de las cosas que se repitan": a library package `httpx/openapi` with the project-agnostic glue, adding no oapi-codegen dependency to the vogel module:
  - strict-handler errors that carry their own vogel writer (go-safo `writtenError`): build one, build one from a `*request.Validator`, write it, and the `ResponseErrorHandlerFunc` / `RequestErrorHandlerFunc` a strict server needs (unknown errors log and answer vogel's 500 envelope);
  - handing the `*http.Request` to strict handlers through the context (complex `RequestInContext` / `requestFrom`), so handlers keep using `request.Validator` and `response` writers;
  - serving the raw embedded spec (`application/yaml`);
  - running a vogel body check (default `request.JSON`) before the generated decoder and rewinding the body (go-safo `decodeOperationBody`).
- S2 Test helpers in `httpx/openapi/openapitest`: spec ↔ router match in both directions over `chi.Walk` (prefix, ignored routes, trailing slash), and schema ↔ Go type match (properties equal, required exactly the fields without omitempty).
- S3 "genera el boiler con open api": the boilerplate becomes contract-first: `api/openapi.yaml` (3.1) is the source of truth, oapi-codegen v2.8.0 pinned as a `go tool`, `go generate` produces models + strict server. Decision 2026-10-27 "Rutas a mano": routes stay registered by hand in chi with their RequirePermission and rate limits, calling the strict server; a test keeps spec and router in sync. swaggo (annotations, docs/docs.go, swagger.json, swagger.yaml, http-swagger) is removed; `SWAGGER_ENABLED` keeps its rules (default off, refused in production) and now serves `GET /openapi.yaml`.
- S4 Invariant (assumption, from the 2026-10-26 decision to keep HTTP responses unchanged): every existing route answers the same status and body as on master for the same request, except the removed `/swagger/*`. Health probes stay outside the contract (assumption, same as complex).
- S5 bpms routes are part of the contract, wrapped in `vogel:component bpms` markers, with their generated code in a package deleted with the component; every check-generated.sh combination builds and tests. Supersedes issue #148 (assumption).
- S6 Tooling: Makefile `generate`, `lint-api` (Redocly), `mock-api` (Prism); CI fails on generated drift (`go generate` + `git status --porcelain`) and lints the spec, in the root boilerplate job and the boilerplate's own CI.
- S7 Docs: boilerplate README and USAGE_GUIDE (swag steps replaced), root README, MIGRATION_GUIDE, OWASP checklist references, CHANGELOG Unreleased.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S1 S2 | inline (no writer reason: sequential, dependent units) | done | de8a96a, 056a23b (review advisories); RED (package missing / stale ignore / embedded) → GREEN |
| T2 | S3 S4 S6 | inline | done | eddaf34; 18 audit handler tests unchanged through the strict server; differential 19 requests vs master: 0 differences; check-generated 9/9 OK; redocly valid; generate deterministic |
| T3 | S5 | inline | done | 4f1402b; bpms differential 217 requests (7 error setups × 31 requests) vs master: 0 differences; check-generated 9/9 with regeneration check |
| T4 | S7 | inline | done | 0eb1583 |

## Log

- L1 (2026-10-27) "pero con respecto a go-safo, me gustaria aplicar una idea que probe en ella, el tema de openapi." / "si, armemos un paquete de esto de las cosas que se repitan y genera el boiler con open api"
- L2 (2026-10-27) Wiring choice answered: "Rutas a mano (Recommended)" — complex style: hand-registered chi routes calling the strict server, generated param binding bypassed.
- L3 Exploration: go-safo and complex diverge. go-safo's error-carrying writer (handler/strict.go) needs no per-operation code; complex's `vogelResponse` needs one Visit method per operation, so S1 takes go-safo's. Request-in-context is needed with hand routes because binding is bypassed and handlers read params from the request. Importing oapi-codegen/runtime for the strict middleware type would pull gin/echo/iris into vogel's module graph, so the library ships a plain net/http middleware instead. Boilerplate swagger.json is stale (only /healthz) and rolematrix_test walks every route.
- L4 T1: `httpx/openapi` (Fail, Respond, Invalid, WriteError, ResponseErrorHandler, RequestErrorHandler, RequestInContext/Request, ServeSpec, Decode, DecodeJSON[T]) and `openapitest` (RoutesMatchSpec with Prefix/Ignore, SchemaMatchesType[T]). Decode tees the body while vogel's check reads it and replays it, so it needs no copy of request's size limit. go.yaml.in/yaml/v3 became a direct dependency (no new module).
- L5 T2: boilerplate contract at api/openapi.yaml (generated package apigen, exclude-tags bpms), x-order keeps vogel's JSON field order in generated envelopes. Mutation check: renamed path and renamed envelope field both fail the contract tests. bpms routes are temporarily ignored in TestSpecAndRouterMatch until T3.
- L6 (2026-10-27) Delivery answered: "Un PR, commits por tarea (Recommended)".
- L7 Native review review-fae4a6d6ebd5acb2 (risk, readability, reliability) over 79593b7..eddaf34 approved and acknowledged; advisories applied: embedded structs and stale ignores in openapitest (056a23b), success envelopes and CaseValue pinned, comment on empty generated params (4f1402b).
- L8 T3: bpms has its own generated package bpmsgen (api/oapi-codegen.bpms.yaml with include-tags, go:generate in api/spec_bpms.go), all three in the bpms component paths. create-vogel-app rewrites the module path only in .go/.md/go.mod, so the spec never points x-go-type-import at boilerplate packages: bpms types are generated (x-order keeps JSON order) and process definitions are json.RawMessage. An inline anyOf in a response component does not compile; it is a named schema (BadRequestResponse). ProcessHandler.Mount registers the routes with openapi.Decode checks (caller, case, then body) so the 401/404/400 order is unchanged.
- L9 Full suites: go vet + go test -race ./... green in the library and the boilerplate (vet with -tags=integration too). Local golangci-lint is not run (local binary predates the Go toolchain); CI runs it.
- L10 Native review review-6abb2e706e2138db (risk, readability, reliability) over eddaf34..179cb83 approved and acknowledged; advisories (non-blocking): duplicated caller/case preconditions between the Decode checks and the strict methods, and the Mount wrappers that bypass generated binding deserve a comment. A whole-branch review (79593b7..HEAD) stopped with lens_context_budget_exceeded; the two approved slices cover the branch.
- L11 Independent verifier: S1-S7 PASS, no blockers. Full router differential 258 requests and bpms 660 requests vs master: only /swagger/* (now 404) and /openapi.yaml (now 200) differ. Advisories: SchemaMatchesType treats omitzero and nil-able embedded pointer fields as required; Ignore does not normalize method case (reported as stale); a generated project without bpms keeps a harmless exclude-tags.
- Next: issue + PR (user decision).
