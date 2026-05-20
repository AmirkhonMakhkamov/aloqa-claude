BEGIN;

-- ALOQA-274: quote-reply support, storing the quoted message reference and
-- frozen snapshot payload.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS quoted_message_id uuid NULL,
    ADD COLUMN IF NOT EXISTS quoted_snapshot jsonb NULL;

-- Cascade SoftDelete lookup: all messages quoting a deleted source message.
CREATE INDEX IF NOT EXISTS idx_messages_quoted_message_id
    ON messages (quoted_message_id)
    WHERE quoted_message_id IS NOT NULL;

COMMIT;
