-- Migration: 043_livekit_webhook_events
-- Stores LiveKit webhook event ids so retries are idempotent across API nodes.

BEGIN;

CREATE TABLE call_livekit_webhook_events (
    event_id         text        PRIMARY KEY CHECK (event_id <> ''),
    call_id          uuid        NOT NULL,
    event_type       text        NOT NULL CHECK (event_type <> ''),
    status           text        NOT NULL CHECK (status IN ('processing', 'processed')),
    received_at      timestamptz NOT NULL DEFAULT now(),
    lease_expires_at timestamptz,
    processed_at     timestamptz
);

CREATE INDEX idx_call_livekit_webhook_events_call_received
    ON call_livekit_webhook_events (call_id, received_at DESC);

CREATE INDEX idx_call_livekit_webhook_events_processing_lease
    ON call_livekit_webhook_events (lease_expires_at)
    WHERE status = 'processing';

COMMIT;
