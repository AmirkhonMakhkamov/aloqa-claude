BEGIN;

-- ALOQA-257: forward message support - persist forwarded_from snapshot JSON.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS forwarded_from jsonb DEFAULT NULL;

-- Reverse lookup index: "all forwards of message X". Cheap to maintain, useful for moderation.
CREATE INDEX IF NOT EXISTS idx_messages_forwarded_from_message_id
    ON messages ((forwarded_from->>'message_id'))
    WHERE forwarded_from IS NOT NULL;

COMMIT;
