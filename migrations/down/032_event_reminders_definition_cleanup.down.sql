-- Down migration: 032_event_reminders_definition_cleanup

BEGIN;

ALTER TABLE event_reminders
    ADD COLUMN IF NOT EXISTS reminders_dispatched_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_event_reminders_pending
    ON event_reminders (event_id, user_id, offset_minutes)
    WHERE reminders_dispatched_at IS NULL;

COMMIT;
