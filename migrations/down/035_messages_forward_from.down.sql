BEGIN;

DROP INDEX IF EXISTS idx_messages_forwarded_from_message_id;

ALTER TABLE messages
    DROP COLUMN IF EXISTS forwarded_from;

COMMIT;
