CREATE TABLE IF NOT EXISTS media_relay_edges (
    call_id UUID NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    source_node_id TEXT NOT NULL,
    target_node_id TEXT NOT NULL,
    target_region TEXT NOT NULL,
    control_url TEXT NOT NULL,
    media_url TEXT NOT NULL,
    fanout_strategy TEXT NOT NULL,
    role_scope TEXT NOT NULL,
    status TEXT NOT NULL,
    sticky BOOLEAN NOT NULL DEFAULT TRUE,
    max_participants INTEGER NOT NULL DEFAULT 0,
    priority INTEGER NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (call_id, target_node_id, role_scope)
);

CREATE INDEX IF NOT EXISTS idx_media_relay_edges_workspace
    ON media_relay_edges (workspace_id, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_media_relay_edges_target
    ON media_relay_edges (target_node_id, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_media_relay_edges_source
    ON media_relay_edges (source_node_id, updated_at DESC);
