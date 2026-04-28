-- 007_guest_invites.sql
-- Time-limited guest invite links with optional email restriction and channel scoping.

CREATE TABLE IF NOT EXISTS guest_invites (
    id           UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by   UUID NOT NULL REFERENCES users(id),
    token        VARCHAR(64) NOT NULL UNIQUE,
    email        VARCHAR(255) DEFAULT '',
    channel_ids  UUID[] DEFAULT '{}',
    max_uses     INT NOT NULL DEFAULT 1,
    use_count    INT NOT NULL DEFAULT 0,
    status       VARCHAR(20) NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active', 'used', 'expired', 'revoked')),
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_guest_invite_token ON guest_invites (token);
CREATE INDEX idx_guest_invite_workspace ON guest_invites (workspace_id, created_at DESC);
