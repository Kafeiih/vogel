-- action_attempt records every attempt of an effect job (action.Worker with
-- action.WithAttemptLog): which effect ran for which case event, when, how it
-- ended and what it produced. River's own job rows are operational and are
-- pruned by its retention, so they cannot answer "did the notification for
-- event 7 go out, and on which attempt" a year later; these rows can.
--
-- A row holds references and classifications only: error_class is the Go
-- type of the error and the sentinel it matched, never its message, because
-- messages can carry personal data.
--
-- case_id has no foreign key: action is usable without workflow's tables,
-- like document_record.
--
-- Uniqueness is (job_id, attempt, snooze). River gives a snoozed job its
-- attempt number back, so the run after a snooze reuses the attempt number;
-- snooze (the job's snooze count when the attempt started) keeps the two
-- rows apart. A duplicate write of the same attempt after a worker crash is
-- a no-op (ON CONFLICT DO NOTHING).
--
-- The table is append-only, with the same triggers and SQLSTATE 23001
-- (restrict_violation) as audit/migrations/002_audit_log_append_only.sql.
-- The table owner or a superuser can still disable them. Retention is an
-- explicit, owner-run procedure inside ONE transaction:
--
--   BEGIN;
--   ALTER TABLE action_attempt DISABLE TRIGGER vogel_action_attempt_no_update_delete;
--   ALTER TABLE action_attempt DISABLE TRIGGER vogel_action_attempt_no_truncate;
--   DELETE FROM action_attempt WHERE finished_at < now() - interval '2 years';
--   ALTER TABLE action_attempt ENABLE TRIGGER vogel_action_attempt_no_update_delete;
--   ALTER TABLE action_attempt ENABLE TRIGGER vogel_action_attempt_no_truncate;
--   COMMIT;

-- +goose Up
CREATE TABLE action_attempt (
    id             UUID PRIMARY KEY,
    case_id        UUID   NOT NULL,
    seq            BIGINT NOT NULL,
    effect         TEXT   NOT NULL,
    part           INT    NOT NULL DEFAULT 0,
    attempt        INT    NOT NULL CHECK (attempt >= 1),
    snooze         INT    NOT NULL DEFAULT 0 CHECK (snooze >= 0),
    job_id         BIGINT NOT NULL,
    started_at     TIMESTAMPTZ NOT NULL,
    finished_at    TIMESTAMPTZ NOT NULL,
    outcome        TEXT NOT NULL CHECK (outcome IN ('succeeded','retry','cancelled','snoozed','discarded','unknown_effect')),
    error_class    TEXT NOT NULL DEFAULT '',
    request_id     TEXT NOT NULL DEFAULT '',
    trace_id       TEXT NOT NULL DEFAULT '',
    actor_kind     TEXT NOT NULL DEFAULT '',
    actor_id       TEXT NOT NULL DEFAULT '',
    result_ref     TEXT NOT NULL DEFAULT '',
    result_digest  TEXT NOT NULL DEFAULT '',
    UNIQUE (job_id, attempt, snooze)
);

CREATE INDEX idx_action_attempt_case_seq ON action_attempt (case_id, seq);
CREATE INDEX idx_action_attempt_outcome ON action_attempt (outcome, finished_at DESC)
    WHERE outcome IN ('retry','discarded','unknown_effect');

-- +goose StatementBegin
CREATE FUNCTION vogel_action_attempt_reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'action_attempt is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation',
              HINT = 'action_attempt rows record what already happened and are never modified or removed; see action/migrations/001_create_action_attempt.sql.';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER vogel_action_attempt_no_update_delete
    BEFORE UPDATE OR DELETE ON action_attempt
    FOR EACH ROW EXECUTE FUNCTION vogel_action_attempt_reject_mutation();

CREATE TRIGGER vogel_action_attempt_no_truncate
    BEFORE TRUNCATE ON action_attempt
    FOR EACH STATEMENT EXECUTE FUNCTION vogel_action_attempt_reject_mutation();

-- +goose Down
DROP TABLE action_attempt;
DROP FUNCTION IF EXISTS vogel_action_attempt_reject_mutation();
