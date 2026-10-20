-- +goose Up
-- Metadata of generated PDFs. The bytes live in object storage under
-- storage_key. case_id is a plain identifier, deliberately without a foreign
-- key to workflow_case: the document package is usable without the workflow
-- engine and its migrations.
CREATE TABLE document (
    id                UUID PRIMARY KEY,
    domain            TEXT NOT NULL,
    external_id       TEXT NOT NULL,
    case_id           UUID NULL,
    template_name     TEXT NOT NULL,
    template_version  INT NOT NULL,
    sha256            TEXT NOT NULL,
    storage_key       TEXT NOT NULL UNIQUE,
    verification_key  TEXT NOT NULL UNIQUE,
    size_bytes        BIGINT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL
);

-- Serves ListByCase and a per-case "does a document of template X exist"
-- lookup.
CREATE INDEX idx_document_case_template ON document (case_id, template_name)
    WHERE case_id IS NOT NULL;
CREATE INDEX idx_document_domain_external_id ON document (domain, external_id);

-- +goose Down
DROP TABLE IF EXISTS document;
