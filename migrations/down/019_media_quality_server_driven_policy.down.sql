BEGIN;
ALTER TABLE media_quality_policies DROP COLUMN IF EXISTS server_driven_min_interval_ms;
ALTER TABLE media_quality_policies DROP COLUMN IF EXISTS server_driven_enabled;
COMMIT;
