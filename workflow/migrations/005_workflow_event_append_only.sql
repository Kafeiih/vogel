-- workflow_event is the case history an audit reads: who moved the case,
-- when, from where to where, with which authority. That only holds if an
-- event, once written, never changes. This migration makes the table declare
-- it, copying the audit_log guard (audit/migrations/002_audit_log_append_only.sql).
--
-- What it does: any UPDATE or DELETE of a row, and any TRUNCATE of the table,
-- fails with SQLSTATE 23001 (restrict_violation). The message always starts
-- with "workflow_event is append-only" followed by the rejected operation, so
-- a caller can match on either the code or the prefix. The case_id foreign
-- key changes from ON DELETE CASCADE to ON DELETE RESTRICT, so deleting a case
-- that has events fails (SQLSTATE 23503) instead of erasing its history; a
-- case without events can still be deleted.
--
-- What it does NOT protect against:
--   - The table owner or a superuser can run ALTER TABLE workflow_event
--     DISABLE TRIGGER ... (or DROP TRIGGER) and then rewrite history.
--     Triggers are a guard against accidental and application-level
--     mutation, not against a privileged operator.
--   - Role separation (an application role with INSERT/SELECT only) and
--     shipping rows to external storage are out of scope here.
--
-- Adding a column later with ALTER TABLE ... ADD COLUMN (with a default, or
-- nullable) is not an UPDATE and keeps working; a backfill UPDATE does not.
--
-- Retention: purging old cases is an explicit, owner-run procedure that must
-- disable the triggers, delete the events and then their cases, and re-enable
-- the triggers inside ONE transaction, so the table is never left unguarded:
--
--   BEGIN;
--   ALTER TABLE workflow_event DISABLE TRIGGER vogel_workflow_event_no_update_delete;
--   ALTER TABLE workflow_event DISABLE TRIGGER vogel_workflow_event_no_truncate;
--   DELETE FROM workflow_event WHERE case_id IN (SELECT id FROM workflow_case WHERE closed_at < now() - interval '10 years');
--   DELETE FROM workflow_case WHERE closed_at < now() - interval '10 years';
--   ALTER TABLE workflow_event ENABLE TRIGGER vogel_workflow_event_no_update_delete;
--   ALTER TABLE workflow_event ENABLE TRIGGER vogel_workflow_event_no_truncate;
--   COMMIT;
--
-- Down drops the triggers, then the function, restores ON DELETE CASCADE, and
-- leaves the table and its rows untouched.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION vogel_workflow_event_reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'workflow_event is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation',
              HINT = 'workflow_event rows are the case history and are never modified or removed; see workflow/migrations/005_workflow_event_append_only.sql.';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER vogel_workflow_event_no_update_delete
    BEFORE UPDATE OR DELETE ON workflow_event
    FOR EACH ROW EXECUTE FUNCTION vogel_workflow_event_reject_mutation();

CREATE TRIGGER vogel_workflow_event_no_truncate
    BEFORE TRUNCATE ON workflow_event
    FOR EACH STATEMENT EXECUTE FUNCTION vogel_workflow_event_reject_mutation();

-- The constraint name is Postgres' default for the unnamed REFERENCES in 001.
ALTER TABLE workflow_event DROP CONSTRAINT workflow_event_case_id_fkey;
ALTER TABLE workflow_event ADD CONSTRAINT workflow_event_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES workflow_case(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE workflow_event DROP CONSTRAINT workflow_event_case_id_fkey;
ALTER TABLE workflow_event ADD CONSTRAINT workflow_event_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES workflow_case(id) ON DELETE CASCADE;
DROP TRIGGER IF EXISTS vogel_workflow_event_no_truncate ON workflow_event;
DROP TRIGGER IF EXISTS vogel_workflow_event_no_update_delete ON workflow_event;
DROP FUNCTION IF EXISTS vogel_workflow_event_reject_mutation();
