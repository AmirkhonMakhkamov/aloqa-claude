BEGIN;

DROP INDEX IF EXISTS idx_calls_pinned_participant_user;

ALTER TABLE calls
    DROP COLUMN IF EXISTS pinned_participant_user_id;

COMMIT;
