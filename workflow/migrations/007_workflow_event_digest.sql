-- Per-case hash chain over workflow_event. Every event appended after this
-- migration stores prev_digest, the digest of the case's event at seq - 1,
-- and digest, SHA-256 over prev_digest and every stored column of the event.
-- workflow.Engine.VerifyChain recomputes each digest from the row as it is now
-- and reports the first event whose content or link no longer matches.
--
-- Where the digest is computed: in the database, by the BEFORE INSERT trigger
-- below, not in Go. detail is JSONB, which Postgres stores normalized (key
-- order, whitespace, number spelling: {"b":1,"a":1e2} reads back as
-- {"a": 100, "b": 1}), so a digest over the bytes the engine sent could never
-- be recomputed from the stored row. Hashing the stored values, with the same
-- function the verification uses, keeps one definition of the encoding. The
-- trigger also chains rows inserted by a binary that predates it, and
-- overwrites any prev_digest or digest an INSERT supplies.
--
-- Canonical encoding (vogel_workflow_event_digest), version tag
-- "vogel.workflow_event.v1" first, then each field as an 8-byte big-endian
-- length followed by its bytes (a NULL prev_digest is length -1, no bytes):
-- prev_digest, id, case_id, seq, kind, from_state, to_state, action,
-- actor_id, occurred_at, comment, schema_version, detail, tenant,
-- actor_kind, acting_as, on_behalf_of, via_client, request_id, trace_id,
-- span_id. UUIDs, integers and occurred_at use their Postgres binary send
-- format (occurred_at: microseconds since 2000-01-01 UTC), text is UTF-8,
-- and detail is its jsonb text. Adding a column to workflow_event does not
-- add it to the digest; a later migration that wants it covered needs a new
-- version tag.
--
-- Rows stored before this migration keep NULL digests: they stay unchained,
-- and the first event appended to a case afterwards starts its chain
-- (prev_digest NULL). Nothing is backfilled: 005 makes the table append-only.
--
-- What the chain does NOT protect against:
--   - Someone who can rewrite rows (the table owner, with the 005 triggers
--     disabled) can also recompute every digest after the rewrite, or drop
--     the last events of a case. Detecting that needs a digest kept outside
--     the database (an export, a signed or external log) to compare with.
--   - Appends must run in a transaction (pgxtx) as the engine documents: on
--     a non-transactional db two concurrent appends to one case can leave an
--     event that cannot see its predecessor, which VerifyChain then reports
--     as broken.
--
-- Cost: one extra indexed lookup (the UNIQUE (case_id, seq) index) and one
-- SHA-256 per append. Adding nullable columns does not rewrite the table.
--
-- Down drops the trigger, the functions and both columns; the stored digests
-- are lost.

-- +goose Up
ALTER TABLE workflow_event
    ADD COLUMN prev_digest BYTEA NULL,
    ADD COLUMN digest      BYTEA NULL;

-- +goose StatementBegin
CREATE FUNCTION vogel_workflow_event_field(b bytea) RETURNS bytea
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN b IS NULL THEN int8send(-1) ELSE int8send(length(b)::bigint) || b END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION vogel_workflow_event_digest(prev bytea, e workflow_event) RETURNS bytea
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
CREATE FUNCTION vogel_workflow_event_chain() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    prev bytea;
BEGIN
    SELECT digest INTO prev FROM workflow_event WHERE case_id = NEW.case_id AND seq = NEW.seq - 1;
    NEW.prev_digest := prev;
    NEW.digest := vogel_workflow_event_digest(prev, NEW);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER vogel_workflow_event_chain
    BEFORE INSERT ON workflow_event
    FOR EACH ROW EXECUTE FUNCTION vogel_workflow_event_chain();

-- +goose Down
DROP TRIGGER IF EXISTS vogel_workflow_event_chain ON workflow_event;
DROP FUNCTION IF EXISTS vogel_workflow_event_chain();
DROP FUNCTION IF EXISTS vogel_workflow_event_digest(bytea, workflow_event);
DROP FUNCTION IF EXISTS vogel_workflow_event_field(bytea);
ALTER TABLE workflow_event DROP COLUMN digest, DROP COLUMN prev_digest;
