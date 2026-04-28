BEGIN;
DROP INDEX IF EXISTS idx_notifications_unread;
DROP INDEX IF EXISTS idx_notifications_user_workspace;
DROP TABLE IF EXISTS notifications;
COMMIT;
