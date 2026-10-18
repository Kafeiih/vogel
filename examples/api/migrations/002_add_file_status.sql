-- +goose Up
-- file_status tracks a document's file through the presigned upload flow:
--   none     -> no file was ever confirmed
--   pending  -> the client confirmed an upload; validate_upload has not run
--   ready    -> validate_upload sniffed an allowed type; it may be downloaded
--   rejected -> the object was missing or its content was not allowed
-- file_content_type is the type validate_upload sniffed, the only type a
-- download is ever served with.
ALTER TABLE documents
    ADD COLUMN file_status TEXT NOT NULL DEFAULT 'none'
        CONSTRAINT documents_file_status_check
        CHECK (file_status IN ('none', 'pending', 'ready', 'rejected')),
    ADD COLUMN file_content_type TEXT;

-- Rows with a key were attached by the old proxying upload, which sniffed
-- the content before storing it.
UPDATE documents SET file_status = 'ready' WHERE storage_key <> '';

-- +goose Down
ALTER TABLE documents
    DROP COLUMN file_content_type,
    DROP COLUMN file_status;
