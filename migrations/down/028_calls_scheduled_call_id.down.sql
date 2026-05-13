-- Down migration: 028_calls_scheduled_call_id

BEGIN;

DROP INDEX IF EXISTS idx_calls_scheduled_call_id;

ALTER TABLE calls
    DROP COLUMN scheduled_call_id;

COMMIT;
