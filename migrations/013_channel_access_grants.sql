CREATE TABLE channel_access_grants (
    id                  UUID PRIMARY KEY,
    channel_id          UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    workspace_id        UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    remote_workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('collaboration_dm')),
    allow_calls         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE channel_access_grants
    ADD CONSTRAINT channel_access_grants_channel_user_unique UNIQUE (channel_id, user_id);

CREATE INDEX idx_channel_access_grants_channel_id ON channel_access_grants (channel_id);
CREATE INDEX idx_channel_access_grants_user_id ON channel_access_grants (user_id);
CREATE INDEX idx_channel_access_grants_workspace_id ON channel_access_grants (workspace_id);
