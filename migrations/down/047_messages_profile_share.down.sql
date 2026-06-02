DROP INDEX IF EXISTS idx_messages_profile_share_user_id;

ALTER TABLE messages
    DROP COLUMN IF EXISTS profile_share;
