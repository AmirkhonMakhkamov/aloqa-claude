BEGIN;
DROP INDEX IF EXISTS idx_media_quality_alerts_call;
DROP INDEX IF EXISTS idx_media_quality_alerts_active_unique;
DROP TABLE IF EXISTS media_quality_alerts;
DROP INDEX IF EXISTS idx_media_quality_policies_workspace;
DROP TABLE IF EXISTS media_quality_policies;
COMMIT;
