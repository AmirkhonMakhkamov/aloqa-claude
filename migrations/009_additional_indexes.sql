-- 009_additional_indexes.sql
-- Performance indexes for common query patterns.

BEGIN;

-- Pinned messages per channel (ListPinned query).
CREATE INDEX idx_messages_channel_pinned ON messages (channel_id, pinned_at DESC)
    WHERE pinned = true AND deleted_at IS NULL;

-- Unread count per channel: COUNT(*) WHERE channel_id=$1 AND user_id!=$2 AND created_at>$3 AND deleted_at IS NULL.
-- The existing idx_messages_channel_active covers (channel_id, created_at) WHERE deleted_at IS NULL.
-- Adding user_id lets Postgres filter without heap lookups.
CREATE INDEX idx_messages_channel_user_created ON messages (channel_id, user_id, created_at)
    WHERE deleted_at IS NULL;

-- Thread reply count: COUNT(*) WHERE parent_id=$1 AND deleted_at IS NULL.
-- Existing idx_messages_parent_id covers parent_id but not deleted_at filter.
CREATE INDEX idx_messages_parent_active ON messages (parent_id)
    WHERE parent_id IS NOT NULL AND deleted_at IS NULL;

-- Recordings: find expired recordings for cleanup.
CREATE INDEX idx_recordings_expires_at ON recordings (expires_at)
    WHERE expires_at IS NOT NULL AND status = 'ready';

COMMIT;
