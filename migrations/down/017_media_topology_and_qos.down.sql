BEGIN;
DROP INDEX IF EXISTS idx_media_qos_samples_user_time;
DROP INDEX IF EXISTS idx_media_qos_samples_workspace_time;
DROP INDEX IF EXISTS idx_media_qos_samples_call_time;
DROP TABLE IF EXISTS media_qos_samples;
DROP INDEX IF EXISTS idx_media_room_placements_node;
DROP INDEX IF EXISTS idx_media_room_placements_workspace;
DROP TABLE IF EXISTS media_room_placements;
COMMIT;
