-- Token/work-item model: parallel branches inside one case. A case holds one
-- or more tokens (workflow_token), each resting on a node, grouped in scopes
-- (workflow_scope): the root scope of the case, plus one scope per split until
-- its join fires. Tokens and scopes are mutable state, like workflow_case;
-- the history stays in workflow_event, whose rows now name their token_id and
-- scope_id.
--
-- Identifiers: the root scope and the root token of a case share the case
-- id, so every case that never splits needs no new identifier, and every
-- existing case is materialized below with exactly that shape.
--
-- Backfill: one root scope and one root token per existing case. An open case
-- gets an active token at its state, with its assignee and deadline, entered
-- by the newest opened/moved event that reached that state (its seq and
-- time; the case's opened_at when there is none). A closed case
-- gets a consumed token and a closed root scope. position and unit stay
-- empty: a node's eligibility lives in the Go Definition, not in SQL. The
-- engine fills them when it loads the case and writes them on the case's next
-- write; workflow.Engine.RefreshEligibility writes them for one case at once.
-- Until then a migrated token does not match InboxFilter.Eligibility (it
-- still matches InboxFilter.States).
--
-- workflow_event.token_id and scope_id are nullable and not backfilled: NULL
-- means an event stored before this migration, which the repository reads as
-- the case id (the root token in the root scope). No row is updated, so the
-- append-only triggers of 005 stay enabled. The hash chain of 007 does not
-- cover the two columns: its digest encodes a fixed column list (version tag
-- "vogel.workflow_event.v1").
--
-- uq_workflow_event_joined_once allows at most one "joined" event per scope,
-- so a join can never fire twice even if the engine's own check regressed.
-- It is built inside this migration's transaction: the build reads the whole
-- workflow_event table and blocks appends while it runs.
--
-- Deploy order: binaries of this version read and write workflow_token and
-- workflow_scope on every call, so apply 008 before starting any of them.
-- Rolling deploys between binaries before and after this version are NOT
-- safe: an older binary moves a case without touching its token, and the
-- tokens drift from the case. Stop every older binary, migrate, then start
-- the new ones. (An open case an older binary created after this migration,
-- with no token rows, gets its root token synthesized by the engine on read
-- and persisted on its next write; that compatibility path is deprecated and
-- goes away in v0.21.0.)
--
-- Down drops both tables and the event columns. It is only lossless before
-- any case split: on a database with split cases it loses the branch state
-- (which tokens are where), and the cases cannot continue.

-- +goose Up
CREATE TABLE workflow_scope (
    id          UUID PRIMARY KEY,
    case_id     UUID NOT NULL REFERENCES workflow_case(id) ON DELETE RESTRICT,
    parent_id   UUID NULL REFERENCES workflow_scope(id) ON DELETE RESTRICT,
    split_node  TEXT NOT NULL DEFAULT '',
    join_node   TEXT NOT NULL DEFAULT '',
    expected    INT  NOT NULL CHECK (expected >= 1),
    status      TEXT NOT NULL CHECK (status IN ('open', 'joined', 'cancelled', 'closed')),
    opened_seq  BIGINT NOT NULL,
    closed_seq  BIGINT NULL,
    CHECK ((parent_id IS NULL) = (id = case_id))
);
CREATE INDEX idx_workflow_scope_case ON workflow_scope (case_id);

CREATE TABLE workflow_token (
    id           UUID PRIMARY KEY,
    case_id      UUID NOT NULL REFERENCES workflow_case(id) ON DELETE RESTRICT,
    scope_id     UUID NOT NULL REFERENCES workflow_scope(id) ON DELETE RESTRICT,
    node         TEXT NOT NULL,
    position     TEXT NOT NULL DEFAULT '',
    unit         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL CHECK (status IN ('active', 'waiting', 'consumed', 'cancelled')),
    assigned_to  TEXT NOT NULL DEFAULT '',
    deadline_at  TIMESTAMPTZ NULL,
    entered_seq  BIGINT NOT NULL,
    entered_at   TIMESTAMPTZ NOT NULL,
    ended_seq    BIGINT NULL,
    ended_at     TIMESTAMPTZ NULL,
    CHECK ((status IN ('active', 'waiting')) = (ended_seq IS NULL))
);
CREATE INDEX idx_workflow_token_case_status ON workflow_token (case_id, status);
CREATE INDEX idx_workflow_token_inbox ON workflow_token (node, position, unit) WHERE status = 'active';
CREATE INDEX idx_workflow_token_assigned ON workflow_token (assigned_to) WHERE status = 'active' AND assigned_to <> '';
CREATE INDEX idx_workflow_token_deadline ON workflow_token (deadline_at) WHERE status = 'active' AND deadline_at IS NOT NULL;

ALTER TABLE workflow_event ADD COLUMN token_id UUID NULL, ADD COLUMN scope_id UUID NULL;
CREATE UNIQUE INDEX uq_workflow_event_joined_once ON workflow_event (scope_id) WHERE kind = 'joined';

INSERT INTO workflow_scope (id, case_id, parent_id, expected, status, opened_seq, closed_seq)
SELECT c.id, c.id, NULL, 1,
       CASE WHEN c.status = 'open' THEN 'open' ELSE 'closed' END,
       1,
       CASE WHEN c.status = 'open' THEN NULL ELSE c.last_seq END
FROM workflow_case c;

INSERT INTO workflow_token (id, case_id, scope_id, node, status, assigned_to, deadline_at,
                            entered_seq, entered_at, ended_seq, ended_at)
SELECT c.id, c.id, c.id, c.state,
       CASE WHEN c.status = 'open' THEN 'active' ELSE 'consumed' END,
       CASE WHEN c.status = 'open' THEN c.assigned_to ELSE '' END,
       CASE WHEN c.status = 'open' THEN c.deadline_at END,
       COALESCE(entered.seq, 1),
       COALESCE(entered.occurred_at, c.opened_at),
       CASE WHEN c.status = 'open' THEN NULL ELSE GREATEST(c.last_seq, 1) END,
       CASE WHEN c.status = 'open' THEN NULL ELSE COALESCE(c.closed_at, c.opened_at) END
FROM workflow_case c
LEFT JOIN LATERAL (
    SELECT e.seq, e.occurred_at FROM workflow_event e
    WHERE e.case_id = c.id AND e.to_state = c.state AND e.kind IN ('opened', 'moved')
    ORDER BY e.seq DESC
    LIMIT 1
) entered ON true;

-- +goose Down
DROP INDEX IF EXISTS uq_workflow_event_joined_once;
ALTER TABLE workflow_event DROP COLUMN scope_id, DROP COLUMN token_id;
DROP TABLE workflow_token;
DROP TABLE workflow_scope;
