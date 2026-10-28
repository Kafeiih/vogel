-- +goose Up
-- last_seq: Seq of the newest event of each case. The engine assigns the next
-- Seq from it and advances it through the revision compare-and-swap, so an
-- append no longer reads the whole history. Backfilled from the events already
-- stored. tenant: scopes the case; '' for a single-tenant consumer and for
-- every case opened before this migration.
ALTER TABLE workflow_case
    ADD COLUMN tenant   TEXT   NOT NULL DEFAULT '',
    ADD COLUMN last_seq BIGINT NOT NULL DEFAULT 0;
UPDATE workflow_case c
   SET last_seq = COALESCE((SELECT MAX(e.seq) FROM workflow_event e WHERE e.case_id = c.id), 0);

-- A case is unique per (tenant, domain, external_id). The dropped constraint
-- is the name Postgres gave the unnamed UNIQUE (domain, external_id) of 001.
ALTER TABLE workflow_case DROP CONSTRAINT workflow_case_domain_external_id_key;
CREATE UNIQUE INDEX uq_workflow_case_tenant_domain_external_id
    ON workflow_case (tenant, domain, external_id);

-- schema_version: rows written before this migration read as 1. detail: the
-- kind-specific JSON object (references and digests only, never domain data).
-- tenant: copied from the case on every append. actor_kind: rows written
-- before this migration read as 'unspecified'; acting_as, on_behalf_of and
-- via_client complete the structured actor whose id stays in actor_id.
-- request_id, trace_id, span_id: the correlation ids of the appending call.
ALTER TABLE workflow_event
    ADD COLUMN schema_version SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN detail         JSONB    NOT NULL DEFAULT '{}',
    ADD COLUMN tenant         TEXT     NOT NULL DEFAULT '',
    ADD COLUMN actor_kind     TEXT     NOT NULL DEFAULT 'unspecified',
    ADD COLUMN acting_as      TEXT     NOT NULL DEFAULT '',
    ADD COLUMN on_behalf_of   TEXT     NOT NULL DEFAULT '',
    ADD COLUMN via_client     TEXT     NOT NULL DEFAULT '',
    ADD COLUMN request_id     TEXT     NOT NULL DEFAULT '',
    ADD COLUMN trace_id       TEXT     NOT NULL DEFAULT '',
    ADD COLUMN span_id        TEXT     NOT NULL DEFAULT '';

-- +goose Down
-- Restoring UNIQUE (domain, external_id) fails while two tenants share a
-- (domain, external_id); resolve those cases before rolling back.
ALTER TABLE workflow_event
    DROP COLUMN span_id, DROP COLUMN trace_id, DROP COLUMN request_id,
    DROP COLUMN via_client, DROP COLUMN on_behalf_of, DROP COLUMN acting_as,
    DROP COLUMN actor_kind, DROP COLUMN tenant, DROP COLUMN detail, DROP COLUMN schema_version;
DROP INDEX IF EXISTS uq_workflow_case_tenant_domain_external_id;
ALTER TABLE workflow_case ADD CONSTRAINT workflow_case_domain_external_id_key UNIQUE (domain, external_id);
ALTER TABLE workflow_case DROP COLUMN last_seq, DROP COLUMN tenant;
