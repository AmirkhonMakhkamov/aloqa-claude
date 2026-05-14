-- Down migration: 031_event_reminder_dispatches

BEGIN;

DROP INDEX IF EXISTS ix_reminder_dispatches_pending;
DROP TABLE IF EXISTS event_reminder_dispatches;

COMMIT;
