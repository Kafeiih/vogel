Objective: the boilerplate's optional bpms component scaffolds a layered BPMS domain (the "consulta" process) on the same infra; examples/bpms is moved into it and deleted.
Branch: feat/boilerplate-bpms-domain (from master e1e3040). Delivery: ask-on-risk (forecast > 400 lines; chain strategy asked before push). Test runner: go test (examples/boilerplate), go test -tags integration, cmd/create-vogel-app/check-generated.sh.

## Specs

- S1 "queria que armaras un boilerplate pero de bpms, un sistema de carpetas, de manera ordenada y lista para replicar. la idea es tener el mismo infra, estructura de carpeta y todo como boilerplate pero con una dominio inicializado con bmps"
- S2 Decision "Ampliar el componente bpms": create-vogel-app --with bpms generates the domain in layers (domain / application / infrastructure / interfaces) with the consulta process initialized; infra, Dockerfile, compose, Cerbos and CI are shared, not copied.
- S3 Decision "Moverlo y borrarlo": everything in examples/bpms moves into the component (versioned JSON definitions, field types, CEL rules, ProfileResolver, inbox, claim/submit, value history); examples/bpms, its CI job and its Dependabot entries are deleted.
- S4 (assumption) The boilerplate's own conventions replace the example's shortcuts: the actor is the authenticated principal (auth.FromContext), routes go through RequirePermission with Cerbos policies, bpms tables run as their own goose set; the fake-SAFO HTTP routes are dropped and an in-memory directory seeded from config (BPMS_PROFILE_HOLDERS) stands in for SAFO.
- S5 With bpms off, nothing of it leaks into a generated project (leak scan, check-generated.sh combos).

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S1-S3 | inline | done | 32f9551 (domain/process + unit tests) |
| T2 | S1-S3 | inline | done | 32f9551 (Catalog, Service, ports; integration tests) |
| T3 | S3 S4 | inline | done | 32f9551 + d0c2f61 (repos, migrations set, directory, seed, BPMS_PROFILE_HOLDERS, grants) |
| T4 | S3 S4 | inline | done | 71025f7 (handler + tests, routes, bpms:process/bpms:case policies + 42 Cerbos tests, role matrix) |
| T5 | S2 | inline | done | d0c2f61 + 71025f7 |
| T6 | S2 S5 | inline | done | 71025f7 (paths, leak scan); check-generated.sh all 9 combos OK |
| T7 | S3 | inline | done | 32f9551 (removal) + a14103f (docs, CHANGELOG) |

Route evidence: units are sequential (each layer depends on the previous one), so no Writer trigger; parent writes inline.

## Log

- L1 2026-10-21 user: "mmm no me convence el examples, queria que armaras un boilerplate pero de bpms, un sistema de carpetas, de manera ordenada y lista para replicar. la idea es tener el mismo infra, estructura de carpeta y todo como boilerplate pero con una dominio inicializado con bmps"
- L2 user chose "Ampliar el componente bpms (Recomendado)" and "Moverlo y borrarlo (Recomendado)".
- L3 map: scaffold runs go mod tidy (cel-go pruned when bpms off); no internal/domain yet; leak scan in internal/scaffold/e2e_test.go:120; role matrix must cover every route.
- L4 T1-T3 committed 32f9551 (examples/bpms removed with CI job and Dependabot entries in the same commit). Config RED "cfg.ProfileHolders undefined" then GREEN. Integration: go test -tags integration ./internal/application/... ./internal/infrastructure/bpms/... ok.
- L5 native review review-7c7d9b70b5f4087c (risk, readability, reliability) on 32f9551: CRITICAL R3-profile-holders-ignored (config parsed, never wired). Correction d0c2f61 wires the bpms migration set, directory and NewDomain into cmd/api and adds runtime grants (bpms_db_version read-only; process_version/case_value SELECT+INSERT). Recovered to successor review-996d85dd17aa3137. T4 WIP (handler + tests, Service.Publish/Latest, domain.CompileError for 422 vs 500) stashed as "t4-wip-bpms-http" while the committed-only review runs.
- L6 review-996d85dd17aa3137 approved and acknowledged on d0c2f61. Advisories applied in 71025f7: seed race (RED: duplicate key with 4 concurrent NewDomain; GREEN via InsertFirst ON CONFLICT + reload), holder trim (RED/GREEN). Routes now wired (R3-routes-unwired). Not applied: stripped comments (user's comment policy), domain test reads the infra seed fixture, Publish store+register not atomic.
- L7 71025f7 committed with a failing scaffold test (gofmt alignment after stripping bpms markers); amended with a blank line inside the markers, as the tracing block does. Roles: publish admin; everything else admin + operator. Swagger annotations not added (swag init rewrites ~900 unrelated lines). Slice 2 (71025f7..a14103f) assessed medium/under_budget, escalated item 2 (authz); user granted review.
