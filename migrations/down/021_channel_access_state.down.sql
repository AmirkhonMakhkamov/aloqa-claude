BEGIN;
DROP INDEX IF EXISTS idx_channel_access_state_user_id;
DROP TABLE IF EXISTS channel_access_state;
COMMIT;
