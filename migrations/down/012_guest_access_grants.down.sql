BEGIN;
DROP INDEX IF EXISTS idx_guest_access_user_workspace;
DROP INDEX IF EXISTS idx_guest_access_invite_user;
DROP TABLE IF EXISTS guest_access_grants;
COMMIT;
