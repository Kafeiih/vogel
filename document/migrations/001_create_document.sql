-- +goose Up
-- Metadata of generated PDFs. The bytes live in object storage under
-- storage_key. case_id is a plain identifier, deliberately without a foreign
-- key to workflow_case: the document package is usable without the workflow
-- engine and its migrations. The table name carries the package prefix, like
-- workflow_case and workflow_event, so it never collides with a consumer's
-- own "document" table.
CREATE TABLE document_record (
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
    -- NULL when nothing identifies the producer; UNIQUE ignores NULLs, so
    -- only real source keys are deduplicated.
    source_key        TEXT NULL UNIQUE,
    created_at        TIMESTAMPTZ NOT NULL
);

-- Serves ListByCase and ExistsForCase.
CREATE INDEX idx_document_record_case_template ON document_record (case_id, template_name)
    WHERE case_id IS NOT NULL;
CREATE INDEX idx_document_record_domain_external_id ON document_record (domain, external_id);

-- +goose Down
DROP TABLE IF EXISTS document_record;
