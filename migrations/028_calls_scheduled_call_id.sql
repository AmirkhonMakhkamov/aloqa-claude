-- Migration: 028_calls_scheduled_call_id
-- Link started calls back to the scheduled calendar event atomically.

BEGIN;

ALTER TABLE calls
    ADD COLUMN scheduled_call_id uuid REFERENCES calendar_events (id) ON DELETE SET NULL;

CREATE INDEX idx_calls_scheduled_call_id
    ON calls (scheduled_call_id)
    WHERE scheduled_call_id IS NOT NULL;

COMMIT;
