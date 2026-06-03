-- Calls in chat history: a type='system' message is written into the call's
-- channel/DM timeline when a call ends. The structured call payload (call_id,
-- type, end_reason, duration, participants) is persisted verbatim in this jsonb
-- column; the FE owns the schema (mirrors forwarded_from / quoted_snapshot).
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS call_event jsonb DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_messages_call_event_call_id
    ON messages ((call_event ->> 'call_id'))
    WHERE call_event IS NOT NULL AND deleted_at IS NULL;
