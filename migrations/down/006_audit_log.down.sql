BEGIN;
DROP INDEX IF EXISTS idx_audit_action;
DROP INDEX IF EXISTS idx_audit_actor;
DROP INDEX IF EXISTS idx_audit_workspace_time;
DROP TABLE IF EXISTS audit_log;
COMMIT;
