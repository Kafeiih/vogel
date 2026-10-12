-- +goose Up
-- Row revision for optimistic concurrency control on workflow_case. Unrelated
-- to workflow_case.version (the definition version). BIGINT so a long-lived,
-- frequently updated case can never wrap; existing rows start at 0.
ALTER TABLE workflow_case ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE workflow_case DROP COLUMN revision;
