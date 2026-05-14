-- Migration: 032_event_reminders_definition_cleanup
-- Keep event_reminders as reminder definitions only.

BEGIN;

DROP INDEX IF EXISTS idx_event_reminders_pending;

ALTER TABLE event_reminders
    DROP COLUMN IF EXISTS fire_at,
    DROP COLUMN IF EXISTS reminders_dispatched_at;

COMMIT;
