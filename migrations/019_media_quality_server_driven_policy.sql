ALTER TABLE media_quality_policies
    ADD COLUMN IF NOT EXISTS server_driven_enabled BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE media_quality_policies
    ADD COLUMN IF NOT EXISTS server_driven_min_interval_ms INTEGER NOT NULL DEFAULT 4000;
