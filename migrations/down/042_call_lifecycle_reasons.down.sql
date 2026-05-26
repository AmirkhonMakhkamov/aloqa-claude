BEGIN;

ALTER TABLE call_participants
    DROP COLUMN IF EXISTS left_reason;

ALTER TABLE calls
    DROP COLUMN IF EXISTS end_reason;

COMMIT;
