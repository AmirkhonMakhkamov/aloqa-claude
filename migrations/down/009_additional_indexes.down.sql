BEGIN;
DROP INDEX IF EXISTS idx_recordings_expires_at;
DROP INDEX IF EXISTS idx_messages_parent_active;
DROP INDEX IF EXISTS idx_messages_channel_user_created;
DROP INDEX IF EXISTS idx_messages_channel_pinned;
COMMIT;
