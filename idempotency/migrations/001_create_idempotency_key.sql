-- idempotency_key: one row per (scope, key) an idempotent operation claimed.
--
-- scope says whose and which operation the key belongs to (for HTTP: the
-- authenticated principal, the method and the route), so two callers, or one
-- caller on two operations, never share a key. fingerprint is a hash of the
-- request content: the same key with another fingerprint is a reused key,
-- which callers reject instead of replaying the wrong response.
--
-- status is 'in_progress' while the operation runs and 'completed' once its
-- response (response_status, response_header, response_body) is stored.
-- locked_until is the lease of an in-progress reservation taken outside a
-- transaction (the HTTP middleware): once it passes, another request may take
-- the key over. reservation identifies the request holding that lease, so a
-- slow request whose lease was taken over cannot store or release the new
-- holder's reservation. A reservation taken inside the caller's transaction
-- (idempotency/postgres.Store.Do) has no lease: the row lock and the
-- transaction itself serialize duplicates.
--
-- expires_at bounds how long a key protects against a retry. An expired row
-- counts as absent, so its key can be claimed again; the consumer deletes
-- expired rows with Store.DeleteExpired, through idx_idempotency_key_expires.
--
-- Down drops the table: every stored key and response is lost, so a retry
-- after Down runs its operation again.

-- +goose Up
CREATE TABLE idempotency_key (
    scope           TEXT        NOT NULL,
    key             TEXT        NOT NULL,
    fingerprint     BYTEA       NOT NULL,
    status          TEXT        NOT NULL CHECK (status IN ('in_progress', 'completed')),
    response_status INT         NULL,
    response_header JSONB       NULL,
    response_body   BYTEA       NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_until    TIMESTAMPTZ NULL,
    reservation     UUID        NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope, key),
    CHECK ((status = 'completed') = (response_status IS NOT NULL))
);
CREATE INDEX idx_idempotency_key_expires ON idempotency_key (expires_at);

-- +goose Down
DROP TABLE idempotency_key;
