-- +goose Up
ALTER TABLE workflow_event ADD COLUMN comment TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workflow_event DROP COLUMN comment;
