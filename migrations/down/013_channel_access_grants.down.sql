BEGIN;
DROP INDEX IF EXISTS idx_channel_access_grants_workspace_id;
DROP INDEX IF EXISTS idx_channel_access_grants_user_id;
DROP INDEX IF EXISTS idx_channel_access_grants_channel_id;
DROP TABLE IF EXISTS channel_access_grants;
COMMIT;
