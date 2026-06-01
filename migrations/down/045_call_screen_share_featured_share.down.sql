BEGIN;

DROP INDEX IF EXISTS idx_calls_featured_share_user;

ALTER TABLE calls
    DROP COLUMN IF EXISTS featured_share_user_id;

ALTER TABLE call_participants
    DROP COLUMN IF EXISTS can_screen_share;

COMMIT;
