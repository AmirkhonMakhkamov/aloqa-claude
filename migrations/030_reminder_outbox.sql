-- Migration: 030_reminder_outbox
-- Transactional outbox for calendar reminder fanout.

BEGIN;

CREATE TABLE reminder_outbox (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    reminder_id   uuid        NOT NULL,
    event_id      uuid        NOT NULL,
    occurrence_at timestamptz NOT NULL,
    user_id       uuid        NOT NULL,
    payload_json  jsonb       NOT NULL,
    enqueued_at   timestamptz NOT NULL DEFAULT NOW(),
    published_at  timestamptz,
    attempts      integer     NOT NULL DEFAULT 0
);

CREATE INDEX ix_reminder_outbox_pending
    ON reminder_outbox (enqueued_at)
    WHERE published_at IS NULL;

COMMIT;
