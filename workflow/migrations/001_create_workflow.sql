-- +goose Up
CREATE TABLE workflow_case (
    id                  UUID PRIMARY KEY,
    definition          TEXT NOT NULL,
    version             INT NOT NULL,
    domain              TEXT NOT NULL,
    external_id         TEXT NOT NULL,
    unit                TEXT NOT NULL,
    state               TEXT NOT NULL,
    status              TEXT NOT NULL,
    assigned_to         TEXT NOT NULL DEFAULT '',
    opened_at           TIMESTAMPTZ NOT NULL,
    closed_at           TIMESTAMPTZ NULL,
    deadline_at         TIMESTAMPTZ NULL,
    UNIQUE (domain, external_id)
);

CREATE INDEX idx_workflow_case_domain_status ON workflow_case (domain, status);
CREATE INDEX idx_workflow_case_status_state  ON workflow_case (status, state);
CREATE INDEX idx_workflow_case_assigned_to   ON workflow_case (assigned_to)
    WHERE assigned_to <> '';
CREATE INDEX idx_workflow_case_deadline_at   ON workflow_case (deadline_at)
    WHERE deadline_at IS NOT NULL AND status = 'open';

CREATE TABLE workflow_event (
    id            UUID PRIMARY KEY,
    case_id       UUID NOT NULL REFERENCES workflow_case(id) ON DELETE CASCADE,
    seq           BIGINT NOT NULL,
    kind          TEXT NOT NULL,
    from_state    TEXT NOT NULL DEFAULT '',
    to_state      TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL DEFAULT '',
    actor_id      TEXT NOT NULL DEFAULT '',
    occurred_at   TIMESTAMPTZ NOT NULL,
    -- Gap-free, ordered history per case. Postgres backs this constraint with a
    -- btree index on (case_id, seq), which already serves ListEvents ordering.
    -- Do not add a second index on the same columns.
    UNIQUE (case_id, seq)
);

-- +goose Down
DROP TABLE IF EXISTS workflow_event;
DROP TABLE IF EXISTS workflow_case;
