-- Branch decision of a join policy: workflow_token.outcome is what a token
-- answered when it arrived at a join with a policy (workflow.Node.JoinPolicy),
-- 'approved' or 'rejected'. The engine reads it to decide when a round closes.
-- History keeps the same value in the detail of the token's "waiting" event,
-- which digest v2 (migration 010) already covers, so the event table and the
-- digest do not change.
--
-- The column is nullable and has no default, so adding it rewrites nothing and
-- needs no backfill: a token from before this migration, and any token that
-- never answered, has no outcome.
--
-- Deploy: run this migration before the binaries that use it, because their
-- repository reads and writes the column.
--
-- Down drops the column. Tokens waiting at a policy join then lose their
-- answer, while the waiting events keep theirs. Run it only with no case inside
-- a block whose join declares a policy.

-- +goose Up
ALTER TABLE workflow_token ADD COLUMN outcome TEXT NULL;

-- +goose Down
ALTER TABLE workflow_token DROP COLUMN outcome;
