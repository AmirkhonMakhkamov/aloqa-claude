BEGIN;

DROP INDEX IF EXISTS idx_messages_quoted_message_id;

ALTER TABLE messages
    DROP COLUMN IF EXISTS quoted_message_id,
    DROP COLUMN IF EXISTS quoted_snapshot;

COMMIT;
