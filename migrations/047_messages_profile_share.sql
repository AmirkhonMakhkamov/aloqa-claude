-- ALK-708: typed internal profile-share cards persisted with chat messages.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS profile_share jsonb DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_messages_profile_share_user_id
    ON messages ((profile_share->>'user_id'))
    WHERE profile_share IS NOT NULL AND deleted_at IS NULL;
