BEGIN;
DROP INDEX IF EXISTS idx_workspace_connections_target;
DROP INDEX IF EXISTS idx_workspace_connections_source;
DROP TABLE IF EXISTS workspace_connections;
DROP INDEX IF EXISTS idx_workspace_role_assignments_user;
DROP TABLE IF EXISTS workspace_role_assignments;
DROP INDEX IF EXISTS idx_workspace_role_definitions_workspace;
DROP TABLE IF EXISTS workspace_role_definitions;
COMMIT;
