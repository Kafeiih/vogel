-- +goose NO TRANSACTION
-- Index for Engine.Participated ("the cases this person took part in"): it
-- finds an actor's events, and through them their cases, without scanning
-- the whole history. Rows without an actor (actor_id = '') are left out.
--
-- It is built CONCURRENTLY, so workflow_event keeps accepting appends while
-- it builds; that cannot run inside a transaction, hence NO TRANSACTION. If
-- the build fails midway Postgres leaves an INVALID index behind: drop it
-- (DROP INDEX CONCURRENTLY idx_workflow_event_actor) and run the migration
-- again.

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_event_actor
    ON workflow_event (actor_id, case_id) WHERE actor_id <> '';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_workflow_event_actor;
