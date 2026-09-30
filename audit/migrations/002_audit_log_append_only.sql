-- audit_log is evidence: consumers rebuild "who did what, and what did the
-- record look like at instant T" from its rows and before/after snapshots.
-- That only holds if a row, once written, never changes. Until now that was
-- a convention; this migration makes the table declare it.
--
-- What it does: any UPDATE or DELETE of a row, and any TRUNCATE of the table,
-- fails with SQLSTATE 23001 (restrict_violation). The message always starts
-- with "audit_log is append-only" followed by the rejected operation, so a
-- caller can match on either the code or the prefix. 23001 is used instead of
-- the generic P0001 (raise_exception) because P0001 is what every ad-hoc
-- trigger raises, and it would be indistinguishable from an unrelated one;
-- 23001 sits in the integrity-constraint class, which is what this is: a
-- constraint that forbids a change.
--
-- What it does NOT protect against:
--   - The table owner or a superuser can run ALTER TABLE audit_log DISABLE
--     TRIGGER ... (or DROP TRIGGER) and then rewrite history. Triggers are a
--     guard against accidental and application-level mutation, not against a
--     privileged operator.
--   - Role separation (an application role with INSERT/SELECT only), hash
--     chaining, and shipping rows to external storage are out of scope here.
--
-- Retention: purging old rows is an explicit, owner-run procedure that must
-- disable the triggers, delete, and re-enable them inside ONE transaction, so
-- the table is never left unguarded:
--
--   BEGIN;
--   ALTER TABLE audit_log DISABLE TRIGGER vogel_audit_log_no_update_delete;
--   ALTER TABLE audit_log DISABLE TRIGGER vogel_audit_log_no_truncate;
--   DELETE FROM audit_log WHERE created_at < now() - interval '7 years';
--   ALTER TABLE audit_log ENABLE TRIGGER vogel_audit_log_no_update_delete;
--   ALTER TABLE audit_log ENABLE TRIGGER vogel_audit_log_no_truncate;
--   COMMIT;
--
-- Down drops the triggers, then the function, and leaves the table and its
-- rows untouched.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION vogel_audit_log_reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation',
              HINT = 'audit_log rows are evidence and are never modified or removed; see audit/migrations/002_audit_log_append_only.sql.';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER vogel_audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION vogel_audit_log_reject_mutation();

CREATE TRIGGER vogel_audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION vogel_audit_log_reject_mutation();

-- +goose Down
DROP TRIGGER IF EXISTS vogel_audit_log_no_truncate ON audit_log;
DROP TRIGGER IF EXISTS vogel_audit_log_no_update_delete ON audit_log;
DROP FUNCTION IF EXISTS vogel_audit_log_reject_mutation();
