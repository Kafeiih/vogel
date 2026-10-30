-- Expiry mark of a token (workflow.Engine.Expire): expired_deadline_at is the
-- deadline_at value Expire recorded as expired. A token counts as expired
-- while it equals deadline_at, so a new deadline (entering a node, or
-- Engine.SetDeadline, which also clears it) makes the token expirable again.
-- Engine.Overdue lists active tokens with deadline_at before a cutoff and not
-- expired, through idx_workflow_token_deadline (migration 008).
--
-- The column is nullable and has no default, so adding it rewrites nothing.
-- Keying the mark on the deadline value, rather than a flag, keeps rolling
-- deploys safe: an older binary that moves a token does not write the
-- column, but the token's new deadline no longer matches the stale mark.
--
-- Down drops the column; the expired events stay in workflow_event, but
-- Overdue then lists tokens that already expired again.

-- +goose Up
ALTER TABLE workflow_token ADD COLUMN expired_deadline_at TIMESTAMPTZ NULL;

-- +goose Down
ALTER TABLE workflow_token DROP COLUMN expired_deadline_at;
