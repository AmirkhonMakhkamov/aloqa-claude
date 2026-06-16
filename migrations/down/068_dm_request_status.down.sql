DROP INDEX IF EXISTS idx_channel_members_user_dm_request_status;

ALTER TABLE channel_members
    DROP COLUMN IF EXISTS dm_request_status;
