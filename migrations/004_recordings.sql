-- Migration: 004_recordings
-- Adds call recording support.

BEGIN;

CREATE TABLE recordings (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    call_id      uuid        NOT NULL REFERENCES calls (id) ON DELETE CASCADE,
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    started_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    format       text        NOT NULL DEFAULT 'webm'
                             CHECK (format IN ('webm', 'mp4')),
    status       text        NOT NULL DEFAULT 'recording'
                             CHECK (status IN ('recording', 'processing', 'ready', 'failed')),
    duration     int,
    file_size    bigint,
    storage_path text,
    legal_hold   boolean     NOT NULL DEFAULT false,
    started_at   timestamptz NOT NULL DEFAULT now(),
    stopped_at   timestamptz,
    ready_at     timestamptz,
    expires_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_recordings_call_id ON recordings (call_id);
CREATE INDEX idx_recordings_workspace_id ON recordings (workspace_id);
CREATE INDEX idx_recordings_status ON recordings (status)
    WHERE status IN ('recording', 'processing');
CREATE INDEX idx_recordings_legal_hold ON recordings (workspace_id)
    WHERE legal_hold = true;

COMMIT;
