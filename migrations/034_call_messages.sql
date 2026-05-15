-- Migration: 034_call_messages
-- Add in-call messages.

BEGIN;

CREATE TABLE call_messages (
    id          uuid        PRIMARY KEY,
    call_id     uuid        NOT NULL REFERENCES calls (id) ON DELETE CASCADE,
    sender_id   uuid        NOT NULL REFERENCES users (id),
    body        text        NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 2000),
    created_at  timestamptz NOT NULL DEFAULT NOW(),
    deleted_at  timestamptz
);

CREATE INDEX idx_call_messages_call_id_id ON call_messages (call_id, id DESC);
CREATE INDEX idx_call_messages_sender_id ON call_messages (sender_id);

COMMIT;
