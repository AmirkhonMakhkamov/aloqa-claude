DROP INDEX IF EXISTS idx_messages_call_event_call_id;

ALTER TABLE messages
    DROP COLUMN IF EXISTS call_event;
