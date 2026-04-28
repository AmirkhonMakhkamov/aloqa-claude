-- 006_audit_log.sql
-- Immutable audit trail for security-relevant workspace events.

CREATE TABLE IF NOT EXISTS audit_log (
    id           UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    actor_id     UUID NOT NULL REFERENCES users(id),
    action       VARCHAR(100) NOT NULL,
    target_type  VARCHAR(50) DEFAULT '',
    target_id    VARCHAR(255) DEFAULT '',
    metadata     JSONB DEFAULT '{}',
    ip_address   VARCHAR(45) DEFAULT '',
    user_agent   TEXT DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_workspace_time ON audit_log (workspace_id, created_at DESC);
CREATE INDEX idx_audit_actor ON audit_log (workspace_id, actor_id, created_at DESC);
CREATE INDEX idx_audit_action ON audit_log (workspace_id, action, created_at DESC);
