ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS storage_tier TEXT NOT NULL DEFAULT 'hot'
        CHECK (storage_tier IN ('hot', 'warm', 'archive')),
    ADD COLUMN IF NOT EXISTS storage_class TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tier_updated_at TIMESTAMPTZ;

UPDATE recordings
SET storage_tier = 'hot',
    tier_updated_at = COALESCE(tier_updated_at, ready_at, stopped_at, created_at)
WHERE storage_tier IS NULL
   OR tier_updated_at IS NULL;

ALTER TABLE recording_artifacts
    ADD COLUMN IF NOT EXISTS storage_tier TEXT NOT NULL DEFAULT 'hot'
        CHECK (storage_tier IN ('hot', 'warm', 'archive')),
    ADD COLUMN IF NOT EXISTS storage_class TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tier_updated_at TIMESTAMPTZ;

UPDATE recording_artifacts
SET storage_tier = 'hot',
    tier_updated_at = COALESCE(tier_updated_at, created_at)
WHERE storage_tier IS NULL
   OR tier_updated_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_recordings_lifecycle
    ON recordings (status, storage_tier, ready_at, tier_updated_at)
    WHERE status = 'ready' AND legal_hold = false;

CREATE INDEX IF NOT EXISTS idx_recording_artifacts_lifecycle
    ON recording_artifacts (recording_id, storage_tier, tier_updated_at);
