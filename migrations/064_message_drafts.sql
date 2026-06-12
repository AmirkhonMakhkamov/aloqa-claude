-- Server-backed message drafts so unsent text follows the user across devices
-- (ALOQA-247 draft sync). Per user, per channel, and per thread root.
CREATE TABLE IF NOT EXISTS message_drafts (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    channel_id        uuid        NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id           uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    parent_message_id uuid        REFERENCES messages (id) ON DELETE CASCADE,
    content           jsonb       NOT NULL,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- One channel-composer draft and one draft per thread root, per user.
CREATE UNIQUE INDEX IF NOT EXISTS uq_message_drafts_channel
    ON message_drafts (user_id, channel_id)
    WHERE parent_message_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_message_drafts_thread
    ON message_drafts (user_id, channel_id, parent_message_id)
    WHERE parent_message_id IS NOT NULL;

-- Hydration query: all of a user's drafts in a workspace.
CREATE INDEX IF NOT EXISTS idx_message_drafts_workspace_user
    ON message_drafts (workspace_id, user_id);
