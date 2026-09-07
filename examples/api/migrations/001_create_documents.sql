-- +goose Up
CREATE TABLE documents (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL,
    kind TEXT NOT NULL,
    issued_at DATE NOT NULL,
    storage_key TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_documents_owner_id ON documents (owner_id);
CREATE INDEX idx_documents_created_at ON documents (created_at DESC);

-- +goose Down
DROP TABLE documents;
