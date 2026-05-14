-- Down migration: 030_reminder_outbox

BEGIN;

DROP INDEX IF EXISTS ix_reminder_outbox_pending;
DROP TABLE IF EXISTS reminder_outbox;

COMMIT;
