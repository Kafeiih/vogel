#!/bin/sh
# Creates the two application roles the deploy compose connects with. The
# official postgres image runs every *.sh in /docker-entrypoint-initdb.d once,
# as the superuser, only when the data directory is empty (first start of the
# volume). Changing a password later needs ALTER ROLE by hand, or a fresh
# volume.
#
#   app_migrator  owns the schemas and tables: only the one-shot "migrate"
#                 service (./api migrate up) logs in with it.
#   app_runtime   owns nothing: api and worker log in with it. It cannot
#                 create, alter or drop objects, so it cannot disable
#                 audit_log's append-only triggers.
#
# The table-level grants that depend on objects created by the migrations
# (audit_log, the goose version tables, River's schema) are applied after
# every "migrate up" by the "grants" service with runtime-grants.sql.
#
# The passwords come from the environment (deploy/.env through compose); this
# file holds no secret. The image may source this script instead of
# executing it, so it neither changes shell options nor calls exit: a missing
# variable fails through ${VAR:?}, which stops the entrypoint.

psql -v ON_ERROR_STOP=1 \
	--username "$POSTGRES_USER" \
	--dbname "$POSTGRES_DB" \
	-v migrator_password="${APP_MIGRATOR_PASSWORD:?APP_MIGRATOR_PASSWORD is required}" \
	-v runtime_password="${APP_RUNTIME_PASSWORD:?APP_RUNTIME_PASSWORD is required}" \
	-v dbname="$POSTGRES_DB" <<'SQL'
CREATE ROLE app_migrator LOGIN PASSWORD :'migrator_password';
CREATE ROLE app_runtime LOGIN PASSWORD :'runtime_password';

-- Nobody but these two roles (and the superuser) connects to this database.
REVOKE ALL ON DATABASE :"dbname" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"dbname" TO app_migrator, app_runtime;
-- CREATE on the database lets the migrator create River's schema
-- (WORKER_SCHEMA) and the trusted pgcrypto extension. app_runtime gets none.
GRANT CREATE ON DATABASE :"dbname" TO app_migrator;

-- PostgreSQL < 15 lets PUBLIC create in schema public; 15+ already does not.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT CREATE, USAGE ON SCHEMA public TO app_migrator;
GRANT USAGE ON SCHEMA public TO app_runtime;

-- Every table and sequence the migrator creates in public from now on
-- (this application's own tables) is readable and writable by the runtime
-- role. runtime-grants.sql then narrows audit_log back to SELECT and INSERT,
-- and the goose version tables to SELECT, after every migration run.
ALTER DEFAULT PRIVILEGES FOR ROLE app_migrator IN SCHEMA public
	GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE app_migrator IN SCHEMA public
	GRANT USAGE, SELECT ON SEQUENCES TO app_runtime;
SQL
