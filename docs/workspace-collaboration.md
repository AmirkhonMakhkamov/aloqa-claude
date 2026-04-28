# Workspace Collaboration

Workspace is the primary boundary in the platform. Users, channels, calls, recordings, search, notifications, marketplace installs, AI jobs, and branding must be scoped to a workspace unless an explicit workspace collaboration policy allows a cross-workspace action.

## Collaboration Model

- `workspace_connections` links two workspaces with a status and JSON policy.
- A connection must be active before cross-workspace directory visibility or contact is allowed.
- Directory visibility can be `none`, `basic`, or `directory`.
- Contact policy can be `none`, `role_based`, `mutual_allow`, or `open`.
- Role-based contact requires both source and target roles to be allowed.

## Example

- Parent workspace connects to a subsidiary workspace.
- Policy allows source roles `owner` and `admin` to contact target roles `owner` and `admin`.
- A subsidiary intern/guest can see only what directory policy allows and cannot directly message the parent-company CEO unless a policy explicitly grants that path.

## Permissions

- `internal/security/rbac` defines permission constants and default workspace role permissions.
- Existing fixed roles remain useful as base roles.
- Custom role definitions and assignments can be layered per workspace via the new RBAC migration.

## Next Implementation Steps

- Add HTTP/admin endpoints for workspace owners to request, approve, pause, and revoke workspace connections.
- Apply `collaboration.Service.CanContact` before creating cross-workspace DMs, shared channels, or external call invitations.
- Add audit log events for every policy change and cross-workspace contact decision.
