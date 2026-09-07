-- +goose Up
CREATE TABLE audit_log (
    id                  UUID PRIMARY KEY,
    resource_type       TEXT NOT NULL,
    resource_id         TEXT NOT NULL,
    aggregate_type      TEXT,
    aggregate_id        TEXT,
    operation_category  TEXT NOT NULL,
    operation_name      TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'success',
    actor_id            TEXT NOT NULL,
    username            TEXT NOT NULL,
    source              TEXT NOT NULL DEFAULT 'http',
    request_id          TEXT,
    ip                  TEXT DEFAULT '',
    user_agent          TEXT DEFAULT '',
    object_repr         TEXT DEFAULT '',
    input_payload       JSONB,
    before_snapshot     JSONB,
    after_snapshot      JSONB,
    changed_fields      JSONB,
    affected_resources  JSONB,
    error_code          TEXT,
    error_message       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_resource  ON audit_log (resource_type, resource_id);
CREATE INDEX idx_audit_log_aggregate ON audit_log (aggregate_type, aggregate_id)
    WHERE aggregate_type IS NOT NULL;
CREATE INDEX idx_audit_log_actor     ON audit_log (actor_id);
CREATE INDEX idx_audit_log_category  ON audit_log (operation_category);
CREATE INDEX idx_audit_log_name      ON audit_log (operation_name);
CREATE INDEX idx_audit_log_status    ON audit_log (status);
CREATE INDEX idx_audit_log_created   ON audit_log (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
