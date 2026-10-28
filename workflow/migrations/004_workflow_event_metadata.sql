-- +goose Up
-- Seq of the newest event of each case. The engine assigns the next Seq from
-- it and advances it through the revision compare-and-swap, so an append no
-- longer reads the whole history. Backfilled from the events already stored.
ALTER TABLE workflow_case ADD COLUMN last_seq BIGINT NOT NULL DEFAULT 0;
UPDATE workflow_case c
   SET last_seq = COALESCE((SELECT MAX(e.seq) FROM workflow_event e WHERE e.case_id = c.id), 0);

-- schema_version: rows written before this migration read as 1. detail: the
-- kind-specific JSON object (references and digests only, never domain data).
ALTER TABLE workflow_event
    ADD COLUMN schema_version SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN detail         JSONB    NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE workflow_event DROP COLUMN detail, DROP COLUMN schema_version;
ALTER TABLE workflow_case DROP COLUMN last_seq;
