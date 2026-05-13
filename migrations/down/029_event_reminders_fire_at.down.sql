-- Down migration: 029_event_reminders_fire_at

BEGIN;

DROP INDEX IF EXISTS idx_event_reminders_pending;

CREATE INDEX idx_event_reminders_pending
    ON event_reminders (event_id, user_id, offset_minutes)
    WHERE reminders_dispatched_at IS NULL;

ALTER TABLE event_reminders
    DROP COLUMN fire_at;

COMMIT;
