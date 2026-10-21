-- +goose Up
-- One row per published version of a process spec. A row is never updated:
-- publishing again inserts the next version.
CREATE TABLE process_version (
    name TEXT NOT NULL,
    version INT NOT NULL CHECK (version >= 1),
    spec JSONB NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (name, version)
);

-- Append-only record of every value a case collected. The current value of
-- a field is its latest row.
CREATE TABLE case_value (
    id BIGSERIAL PRIMARY KEY,
    case_id UUID NOT NULL,
    field TEXT NOT NULL,
    value JSONB NOT NULL,
    node_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_case_value_case_field ON case_value (case_id, field, id DESC);

-- +goose Down
DROP TABLE case_value;
DROP TABLE process_version;
