-- Migration: 029_event_reminders_fire_at
-- Persist the concrete reminder due time so workers can claim pending rows safely.

BEGIN;

ALTER TABLE event_reminders
    ADD COLUMN fire_at timestamptz;

UPDATE event_reminders r
SET fire_at = e.scheduled_at - make_interval(mins => r.offset_minutes)
FROM calendar_events e
WHERE r.event_id = e.id
  AND r.fire_at IS NULL;

ALTER TABLE event_reminders
    ALTER COLUMN fire_at SET NOT NULL;

DROP INDEX IF EXISTS idx_event_reminders_pending;

CREATE INDEX idx_event_reminders_pending
    ON event_reminders (fire_at, id)
    WHERE reminders_dispatched_at IS NULL;

COMMIT;
