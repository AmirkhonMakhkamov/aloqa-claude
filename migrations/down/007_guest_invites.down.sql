BEGIN;
DROP INDEX IF EXISTS idx_guest_invite_workspace;
DROP INDEX IF EXISTS idx_guest_invite_token;
DROP TABLE IF EXISTS guest_invites;
COMMIT;
