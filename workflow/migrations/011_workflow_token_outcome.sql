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
-- repository reads and writes the column. Those binaries write event schema 5
-- for every case, which older binaries refuse to read, so replace them all
-- together and fix forward rather than rolling the binaries back.
--
-- Down drops the column, which the current binaries need, so they fail on every
-- token write until it is back. Tokens waiting at a policy join lose their
-- answer, while the waiting events keep theirs: after applying this migration
-- again, such a round fails with workflow.ErrInvalidOutcome instead of counting
-- the vote, until the answers are restored (docs/MIGRATION_GUIDE.md has the
-- statement). Run Down only with no case inside a block whose join has a policy.

-- +goose Up
ALTER TABLE workflow_token ADD COLUMN outcome TEXT NULL;

-- +goose Down
ALTER TABLE workflow_token DROP COLUMN outcome;
