-- Table-level grants for app_runtime, applied by the deploy compose's
-- "grants" service as app_migrator (the owner of every object named here)
-- right after each "./api migrate up". Every statement is idempotent, so it
-- runs on every deploy and also covers tables a new migration added.
--
-- This file is NOT an init script: the compose mounts only 10-roles.sh into
-- /docker-entrypoint-initdb.d, because the tables below do not exist until
-- the migrations ran.
--
-- vogel:component worker begin
-- psql variable: worker_schema, River's schema (WORKER_SCHEMA, default river).
-- vogel:component worker end

\set ON_ERROR_STOP on

-- audit_log is append-only evidence: the runtime role reads and inserts,
-- nothing else. The default privileges from 10-roles.sh granted it full DML
-- when the migration created the table; take UPDATE and DELETE back. (The
-- triggers already reject them; this is the second line of defence, and as
-- a non-owner app_runtime cannot disable the triggers.)
REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM app_runtime;
GRANT SELECT, INSERT ON audit_log TO app_runtime;

-- goose version tables: "start-from-migrate" (never used by this compose)
-- only reads them when nothing is pending; the runtime never writes them.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON goose_db_version, vogel_db_version FROM app_runtime;
GRANT SELECT ON goose_db_version, vogel_db_version TO app_runtime;
-- vogel:component bpms begin
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON workflow_db_version, document_db_version FROM app_runtime;
GRANT SELECT ON workflow_db_version, document_db_version TO app_runtime;
-- vogel:component bpms end

-- vogel:component worker begin
-- River (api inserts jobs; worker fetches, updates and deletes them). At
-- runtime River's client:
--   * inserts, fetches (SELECT ... FOR UPDATE SKIP LOCKED), updates and
--     deletes rows in river_job (the job cleaner deletes finished jobs);
--   * upserts and deletes river_leader (leader election), river_queue
--     (queue registry and the queue cleaner) and river_client /
--     river_client_queue;
--   * draws river_job ids from its bigserial sequence;
--   * reads river_migration;
--   * uses pg_notify / LISTEN and advisory locks, which need no grant.
-- It never creates or alters tables: "migrate up" did that as app_migrator.
GRANT USAGE ON SCHEMA :"worker_schema" TO app_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA :"worker_schema" TO app_runtime;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA :"worker_schema" TO app_runtime;

-- River's leader also runs a reindexer (REINDEX INDEX CONCURRENTLY on
-- river_job's indexes), which needs table ownership or, from PostgreSQL 17,
-- the MAINTAIN privilege. On older servers the reindexer only logs an error
-- and the queue keeps working.
SELECT current_setting('server_version_num')::int >= 170000 AS has_maintain \gset
\if :has_maintain
GRANT MAINTAIN ON ALL TABLES IN SCHEMA :"worker_schema" TO app_runtime;
\endif
-- vogel:component worker end
