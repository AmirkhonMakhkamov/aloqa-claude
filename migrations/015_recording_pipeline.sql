ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'both'
        CHECK (strategy IN ('composite', 'per_track', 'both')),
    ADD COLUMN IF NOT EXISTS integrity_sha256 TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS downloadable BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS processing_attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS max_processing_attempts INT NOT NULL DEFAULT 5,
    ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ;

ALTER TABLE recordings
    DROP CONSTRAINT IF EXISTS recordings_format_check;
ALTER TABLE recordings
    ADD CONSTRAINT recordings_format_check
        CHECK (format IN ('webm', 'mp4', 'bundle'));

CREATE INDEX IF NOT EXISTS idx_recordings_retry
    ON recordings (status, next_retry_at)
    WHERE status IN ('processing', 'failed');

CREATE TABLE IF NOT EXISTS recording_artifacts (
    id               UUID PRIMARY KEY,
    recording_id     UUID NOT NULL REFERENCES recordings(id) ON DELETE CASCADE,
    workspace_id     UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('audio_track', 'video_track', 'screen_track', 'manifest', 'session_bundle')),
    source_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    track_id         TEXT NOT NULL DEFAULT '',
    stream_id        TEXT NOT NULL DEFAULT '',
    layer            TEXT NOT NULL DEFAULT '',
    codec            TEXT NOT NULL DEFAULT '',
    mime_type        TEXT NOT NULL DEFAULT '',
    format           TEXT NOT NULL DEFAULT '',
    storage_path     TEXT NOT NULL,
    file_size        BIGINT NOT NULL DEFAULT 0,
    integrity_sha256 TEXT NOT NULL DEFAULT '',
    packet_count     BIGINT NOT NULL DEFAULT 0,
    duration         INT,
    downloadable     BOOLEAN NOT NULL DEFAULT false,
    metadata         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_recording_artifacts_recording
    ON recording_artifacts (recording_id, created_at);
CREATE INDEX IF NOT EXISTS idx_recording_artifacts_workspace
    ON recording_artifacts (workspace_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_recording_artifacts_storage_path
    ON recording_artifacts (storage_path);
