-- 010_attachment_storage_path_index.sql
-- Fast and unique authorization lookup for file downloads by storage key.

BEGIN;

CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_storage_path
    ON attachments (storage_path);

COMMIT;
