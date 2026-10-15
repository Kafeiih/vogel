-- One-time upgrade of an EXISTING deploy volume whose migrations ran as the
-- superuser (the deploy compose before app_migrator and app_runtime existed).
-- Run it by hand, as the superuser, after 10-roles.sh created the roles and
-- before the first "migrate up" as app_migrator; the procedure is in
-- deploy/RUNBOOK.md section 0, "Actualizar un volumen existente". It is NOT
-- an init script: a fresh volume never needs it, because there app_migrator
-- creates every object and owns it from the start.
--
-- vogel:component worker begin
-- psql variable: worker_schema, River's schema (WORKER_SCHEMA, default river;
-- pass -v worker_schema=<schema> only if WORKER_SCHEMA was changed).
-- vogel:component worker end
--
-- What it does, in one transaction:
--   1. hands every application object in schema public and, with the worker,
--      in River's schema (tables, views, sequences, routines, types, and
--      River's schema itself)
--      to app_migrator, so later migrations, which run as app_migrator, can
--      alter them, and app_runtime, which owns nothing, cannot;
--   2. gives app_runtime DML on the tables and sequences that already exist
--      in public, the grants 10-roles.sh's default privileges give to every
--      table app_migrator creates from now on. runtime-grants.sql (the
--      "grants" service) then narrows audit_log and the goose tables, and
--      covers River's schema when there is one, exactly as on a fresh volume.
--
-- Why not "REASSIGN OWNED BY postgres TO app_migrator": postgres is the
-- bootstrap superuser and owns the system catalogs, so PostgreSQL refuses to
-- reassign its objects ("required by the database system"); and even for
-- another former owner, REASSIGN OWNED acts on every database-wide object
-- that role owns (the database itself, schema public, extensions, objects in
-- other schemas), well beyond this application's tables. This script names
-- exactly what moves: objects in public (and River's schema) that are not
-- part of an extension. Extension objects (pgcrypto's functions in public)
-- and the extension itself stay with their owner: ALTER EXTENSION has no
-- OWNER TO, and app_migrator only ever runs "CREATE EXTENSION IF NOT
-- EXISTS", which needs no ownership of an extension that already exists.
-- Schema public also keeps its owner (pg_database_owner on PostgreSQL 15+):
-- 10-roles.sh already grants app_migrator CREATE on it.
--
-- Sequences linked to a table column (serial, bigserial, identity) are not
-- altered directly, PostgreSQL rejects that: ALTER TABLE ... OWNER TO moves
-- them with their table. Array types and a table's row type likewise follow
-- their element type or table.
--
-- Running it twice is harmless: the second run finds nothing to move and
-- re-applies the same grants.

\set ON_ERROR_STOP on

BEGIN;

-- vogel:component worker begin
\if :{?worker_schema}
\else
\set worker_schema river
\endif
SELECT set_config('vogel.worker_schema', :'worker_schema', true);

-- vogel:component worker end
DO $$
DECLARE
	target  constant name := 'app_migrator';
	schemas constant name[] := ARRAY['public'
	-- vogel:component worker begin
		, current_setting('vogel.worker_schema')
	-- vogel:component worker end
	]::name[];
	obj     record;
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = target) THEN
		RAISE EXCEPTION 'role % does not exist: run deploy/initdb/10-roles.sh first', target;
	END IF;

	-- vogel:component worker begin
	-- River's schema itself (public keeps its owner, see above).
	FOR obj IN
		SELECT n.nspname
		FROM pg_namespace n
		WHERE n.nspname = current_setting('vogel.worker_schema')
		  AND n.nspname <> 'public'
		  AND n.nspowner <> (SELECT oid FROM pg_roles WHERE rolname = target)
	LOOP
		EXECUTE format('ALTER SCHEMA %I OWNER TO %I', obj.nspname, target);
	END LOOP;

	-- vogel:component worker end
	-- Tables, partitioned tables, views, materialized views, foreign tables
	-- and the sequences not linked to a table column.
	FOR obj IN
		SELECT c.oid::regclass AS rel, c.relkind
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY (schemas)
		  AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
		  AND c.relowner <> (SELECT oid FROM pg_roles WHERE rolname = target)
		  AND NOT EXISTS (  -- not part of an extension
			SELECT 1 FROM pg_depend d
			WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
		  AND NOT (c.relkind = 'S' AND EXISTS (  -- not owned by a table column
			SELECT 1 FROM pg_depend d
			WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid
			  AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')))
		ORDER BY c.relkind, c.oid
	LOOP
		EXECUTE format('ALTER %s %s OWNER TO %I',
			CASE obj.relkind
				WHEN 'v' THEN 'VIEW'
				WHEN 'm' THEN 'MATERIALIZED VIEW'
				WHEN 'f' THEN 'FOREIGN TABLE'
				WHEN 'S' THEN 'SEQUENCE'
				ELSE 'TABLE'
			END,
			obj.rel, target);
	END LOOP;

	-- Functions, procedures and aggregates (audit_log's trigger functions,
	-- River's functions), outside any extension.
	FOR obj IN
		SELECT p.oid::regprocedure AS fn
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = ANY (schemas)
		  AND p.proowner <> (SELECT oid FROM pg_roles WHERE rolname = target)
		  AND NOT EXISTS (
			SELECT 1 FROM pg_depend d
			WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
	LOOP
		EXECUTE format('ALTER ROUTINE %s OWNER TO %I', obj.fn, target);
	END LOOP;

	-- Standalone types: enums (River's river_job_state), domains, ranges and
	-- composite types created with CREATE TYPE. Base and array types and a
	-- table's row type are left alone.
	FOR obj IN
		SELECT t.oid::regtype AS typ, t.typtype
		FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = ANY (schemas)
		  AND t.typtype IN ('e', 'd', 'r', 'c')
		  AND (t.typtype <> 'c'
			OR (SELECT c.relkind FROM pg_class c WHERE c.oid = t.typrelid) = 'c')
		  AND t.typowner <> (SELECT oid FROM pg_roles WHERE rolname = target)
		  AND NOT EXISTS (
			SELECT 1 FROM pg_depend d
			WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
	LOOP
		EXECUTE format('ALTER %s %s OWNER TO %I',
			CASE obj.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END,
			obj.typ, target);
	END LOOP;
END
$$;

-- The tables and sequences that existed before app_migrator owned them
-- never received 10-roles.sh's default privileges. Granted by a superuser,
-- these are recorded as granted by the owner (app_migrator), so the REVOKE
-- in runtime-grants.sql, which runs as app_migrator, narrows them as usual.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_runtime;

COMMIT;
