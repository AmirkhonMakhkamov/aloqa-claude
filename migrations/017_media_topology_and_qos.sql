CREATE TABLE IF NOT EXISTS media_room_placements (
    call_id UUID PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    region TEXT NOT NULL,
    control_url TEXT NOT NULL,
    media_url TEXT NOT NULL,
    routing_mode TEXT NOT NULL,
    fanout_strategy TEXT NOT NULL,
    overflow_policy TEXT NOT NULL,
    screen_share_priority TEXT NOT NULL,
    turn_strategy TEXT NOT NULL DEFAULT '',
    sticky BOOLEAN NOT NULL DEFAULT TRUE,
    max_participants INTEGER NOT NULL DEFAULT 0,
    max_presenters INTEGER NOT NULL DEFAULT 0,
    max_viewers INTEGER NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_media_room_placements_workspace
    ON media_room_placements (workspace_id, assigned_at DESC);

CREATE INDEX IF NOT EXISTS idx_media_room_placements_node
    ON media_room_placements (node_id, assigned_at DESC);

CREATE TABLE IF NOT EXISTS media_qos_samples (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    call_id UUID NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    region TEXT NOT NULL,
    stream_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    participant_role TEXT NOT NULL DEFAULT '',
    media_kind TEXT NOT NULL DEFAULT '',
    packet_loss_pct DOUBLE PRECISION NOT NULL DEFAULT 0,
    jitter_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    round_trip_time_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    available_outgoing_bitrate_kbps INTEGER NOT NULL DEFAULT 0,
    available_incoming_bitrate_kbps INTEGER NOT NULL DEFAULT 0,
    bytes_sent BIGINT NOT NULL DEFAULT 0,
    bytes_received BIGINT NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    sampled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_media_qos_samples_call_time
    ON media_qos_samples (call_id, sampled_at DESC);

CREATE INDEX IF NOT EXISTS idx_media_qos_samples_workspace_time
    ON media_qos_samples (workspace_id, sampled_at DESC);

CREATE INDEX IF NOT EXISTS idx_media_qos_samples_user_time
    ON media_qos_samples (user_id, sampled_at DESC);
