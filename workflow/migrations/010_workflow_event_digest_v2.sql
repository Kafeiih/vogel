-- Hash chain v2 and the deadline a token entered a node with.
--
-- workflow_event.deadline_at is the deadline the event's token got on coming
-- to rest at to_state (workflow.Event.DeadlineAt): set on the opened or moved
-- event that leaves a token at a task node declaring Node.Deadline or
-- Node.DeadlineRule, NULL on every other event. It lets the history rebuild
-- a rule-resolved deadline, which no Definition can recompute later. The
-- column is nullable with no default, so adding it rewrites nothing.
--
-- vogel_workflow_event_digest (migration 007) is replaced by the v2 encoding:
-- version tag "vogel.workflow_event.v2", then the same fields as v1 in the
-- same order and encoding, then token_id and scope_id (migration 008, uuid
-- send format) and deadline_at (timestamptz send format), each NULL as
-- length -1. Every column workflow_event has after this migration, apart
-- from prev_digest and digest themselves, is covered.
--
-- There is no v1/v2 dispatch: the function computes v2 for every row, so
-- VerifyChain reports a row chained with v1 before this migration as broken.
-- vogel/workflow had no v1-chained data to keep verifiable when v2 was
-- introduced; a database that does must not apply this migration as is.
--
-- CREATE OR REPLACE FUNCTION clears the search_path 007 pinned on the
-- function, so both directions pin it again (see 007's "Name resolution").
--
-- Deploy order: the workflow/postgres repository of this version writes and
-- reads deadline_at, so it fails against a database without it. Apply 010
-- before starting any binary of this version.
--
-- Down restores the v1 function and drops the column. Rows appended while
-- 010 was applied keep their v2 digests, which v1 then reports as broken.
-- The entered deadlines are lost: after Down and Up again, every v2 row that
-- carried one stays broken for good, since its digest covered a deadline_at
-- the table no longer holds.

-- +goose Up
ALTER TABLE workflow_event ADD COLUMN deadline_at TIMESTAMPTZ NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION vogel_workflow_event_digest(prev bytea, e workflow_event) RETURNS bytea
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT sha256(
        convert_to('vogel.workflow_event.v2', 'UTF8')
        || vogel_workflow_event_field(prev)
        || vogel_workflow_event_field(uuid_send(e.id))
        || vogel_workflow_event_field(uuid_send(e.case_id))
        || vogel_workflow_event_field(int8send(e.seq))
        || vogel_workflow_event_field(convert_to(e.kind, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.from_state, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.to_state, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.action, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.actor_id, 'UTF8'))
        || vogel_workflow_event_field(timestamptz_send(e.occurred_at))
        || vogel_workflow_event_field(convert_to(e.comment, 'UTF8'))
        || vogel_workflow_event_field(int2send(e.schema_version))
        || vogel_workflow_event_field(convert_to(e.detail::text, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.tenant, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.actor_kind, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.acting_as, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.on_behalf_of, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.via_client, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.request_id, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.trace_id, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.span_id, 'UTF8'))
        || vogel_workflow_event_field(uuid_send(e.token_id))
        || vogel_workflow_event_field(uuid_send(e.scope_id))
        || vogel_workflow_event_field(timestamptz_send(e.deadline_at))
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('ALTER FUNCTION vogel_workflow_event_digest(bytea, workflow_event) SET search_path = %I, pg_temp', current_schema());
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION vogel_workflow_event_digest(prev bytea, e workflow_event) RETURNS bytea
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT sha256(
        convert_to('vogel.workflow_event.v1', 'UTF8')
        || vogel_workflow_event_field(prev)
        || vogel_workflow_event_field(uuid_send(e.id))
        || vogel_workflow_event_field(uuid_send(e.case_id))
        || vogel_workflow_event_field(int8send(e.seq))
        || vogel_workflow_event_field(convert_to(e.kind, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.from_state, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.to_state, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.action, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.actor_id, 'UTF8'))
        || vogel_workflow_event_field(timestamptz_send(e.occurred_at))
        || vogel_workflow_event_field(convert_to(e.comment, 'UTF8'))
        || vogel_workflow_event_field(int2send(e.schema_version))
        || vogel_workflow_event_field(convert_to(e.detail::text, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.tenant, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.actor_kind, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.acting_as, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.on_behalf_of, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.via_client, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.request_id, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.trace_id, 'UTF8'))
        || vogel_workflow_event_field(convert_to(e.span_id, 'UTF8'))
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('ALTER FUNCTION vogel_workflow_event_digest(bytea, workflow_event) SET search_path = %I, pg_temp', current_schema());
END;
$$;
-- +goose StatementEnd

ALTER TABLE workflow_event DROP COLUMN deadline_at;
