Objective: the boilerplate's deploy follows the go-safo pattern: a complete deploy/.env template, RDS for production, an optional bundled Postgres for other environments, one host per environment.
Branch: feat/boilerplate-deploy-rds (on top of 4dff28e, the yaml/env comment cleanup). Delivery: ask-on-risk, one PR. Test runner: go test, cmd/create-vogel-app/check-generated.sh, docker compose config.

## Specs

- S1 "siento que en el deploy el .env esta muy escualido revisa @../go-safo/ ese tiene el patron que uso comunmente": deploy/env.example becomes a complete template of every setting a deployed host needs (app, database, Zitadel, Cerbos, CORS, proxies, and the optional components), grouped by section, with the optional variables listed commented out. It restores the optional variables removed in 4dff28e.
- S2 "uso siempre rds para base de datos productiva": production connects to AWS RDS with sslmode=verify-full and the RDS CA bundle mounted read-only, failing loudly when the bundle file is missing (go-safo's x-rds-ca pattern).
- S3 "tengo varios enviroments" + answer "Un host por ambiente": one template serves every environment; each host keeps its own deploy/.env (APP_ENV, its database, its Zitadel).
- S4 answer "Solo producción usa RDS": the bundled Postgres stays available behind a compose profile for non-production hosts; production does not start it.
- S5 (kept invariant, assumption): the two-role model stays: app_migrator owns the schema and runs migrate and grants; app_runtime serves. On RDS the roles are created once with the master user by the same roles script.
- S6 (kept invariant): create-vogel-app generates every component combination and each renders its compose files (check-generated.sh).

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S2 S4 S5 | inline | done | e412d0c |
| T2 | S5 | inline | done | e412d0c |
| T3 | S1 S3 | inline | done | e412d0c |
| T4 | S1-S6 | inline | done | e412d0c |
| T5 | S6 | inline | done | e412d0c |

- T1 deploy/docker-compose.yaml: postgres under profile bundled-db; DB_URL and MIGRATE_DB_URL from deploy/.env; RDS CA bind mount; optional depends_on on postgres; log rotation.
- T2 roles on RDS: a tools-profile service runs initdb/10-roles.sh against RDS with the master user; the script grants itself app_migrator membership when it is not a superuser.
- T3 deploy/env.example: complete template, restored optional variables.
- T4 docs: RUNBOOK section 0 and the sections that assume the bundled Postgres, README deploy notes, CHANGELOG.
- T5 cmd/create-vogel-app/check-generated.sh and .vogel/template.json follow the new variables.

## Log

- L1 (2026-10-20, user): "ok, falta otra cosa, siento que en el deploy el .env esta muy escualido revisa @../go-safo/ ese tiene el patron que uso comunmente, uso siempre rds para base de datos productiva, tengo varios enviroments"
- L2 (2026-10-20, user answers): environments: "Un host por ambiente"; database: "Solo producción usa RDS".
- L3 analysis: go-safo deploy has no Postgres (RDS via DB_URL, verify-full, x-rds-ca bind mount with create_host_path false, DATABASE_SSL_CA), log rotation (x-logging), migrate under profile tools, full app config in deploy/.env. The boilerplate keeps its bundled Postgres with app_migrator/app_runtime roles, grants service and sslmode=disable. 4dff28e removed commented optional variables from deploy/env.example (regression to fix in T3). pgx reads sslrootcert from the DSN, so no DATABASE_SSL_CA code is needed.
- L4 (2026-10-20) done in e412d0c: compose with profiles bundled-db and tools (db-roles), DSNs from .env, x-rds-ca, x-logging, API_PORT; 10-roles.sh membership grant (RED: old script fails as a non-superuser master with "permission denied to change default privileges"; GREEN on a fresh RDS-like cluster); migrate + grants as app_migrator and app_runtime denied UPDATE on audit_log and CREATE in public; env.example complete; RUNBOOK 0/1/9/10, README, CHANGELOG; check-generated all combinations OK; generated project renames DB_NAME/DSNs. Compose interpolates inactive-profile services, so db-roles checks its vars in the container. COMPOSE_PROFILES in deploy/.env enables bundled-db.
- L5 (2026-10-20): independent verifier PASS S1-S6 (advisories: grants DSN argv, shell history, api restart policy). Follow-ups: e661875 docs, 240f572 db-roles guard + 3 coupling notes, 8b2da66 user chose "Armar el DSN en compose" (DB_HOST/DB_NAME/DB_PORT/DB_SSLMODE + passwords once; grants back on PGPASSWORD), 90e144e bundled POSTGRES_DB from DB_NAME + Compose >= 2.20 note. pgx reads sslrootcert even with sslmode=disable (works because the bundle is always mounted). Every RDD review approved and acknowledged; last commit under_budget. check-generated OK.
- L6 (2026-10-20, user): "dale, despues de esto debes re-revisar el rema del docker-compose.yml te comente que es similar a go-safo donde no levanta un postgres proque produccion funciona con rds". CI on #127 all green, full-range review approved. go-safo deploy/docker-compose.yaml has no postgres; its local Postgres lives only in deploy/docker-compose.developer.yaml (the boilerplate has the same file). #127 NOT merged yet. Open question to the user: do all deployed environments (staging, QA) use their own RDS? If yes, remove postgres, profile bundled-db, volume pgdata and the bundled block in env.example from deploy compose (plus RUNBOOK/README/CHANGELOG/check-generated references) inside #127. Separate PR afterwards: strip comments from boilerplate SQL, .sh, Dockerfiles, Makefile.
- L7 (2026-10-20, user): "push todo seguimos en casa". Branch already pushed; resume from L6 at home.
