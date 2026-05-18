-- Down migration: 034_call_messages

BEGIN;

DROP INDEX IF EXISTS idx_call_messages_sender_id;
DROP INDEX IF EXISTS idx_call_messages_call_id_id;
DROP TABLE IF EXISTS call_messages;

COMMIT;
