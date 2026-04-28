-- 005_notifications.sql
-- In-app notification storage.

CREATE TABLE IF NOT EXISTS notifications (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    type        VARCHAR(50) NOT NULL,
    title       VARCHAR(255) NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    resource_id VARCHAR(255) DEFAULT '',
    read        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_notifications_user_workspace ON notifications (user_id, workspace_id, created_at DESC);
CREATE INDEX idx_notifications_unread ON notifications (user_id, workspace_id) WHERE read = FALSE;
