ALTER TABLE channel_members
    ADD COLUMN IF NOT EXISTS dm_request_status text NOT NULL DEFAULT 'accepted'
        CHECK (dm_request_status IN ('accepted', 'pending', 'blocked'));

CREATE INDEX IF NOT EXISTS idx_channel_members_user_dm_request_status
    ON channel_members (user_id, dm_request_status);
