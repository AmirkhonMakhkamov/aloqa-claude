-- Down migration: 033_event_reminders_definition_constraint

BEGIN;

ALTER TABLE event_reminders
    DROP CONSTRAINT IF EXISTS uq_event_reminders_definition;

CREATE UNIQUE INDEX IF NOT EXISTS idx_event_reminders_unique
    ON event_reminders (event_id, user_id, offset_minutes, channel);

COMMIT;
