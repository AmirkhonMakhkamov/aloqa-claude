-- 011_workspace_rbac_collaboration.sql
-- Flexible workspace permissions and controlled cross-workspace collaboration.

BEGIN;

CREATE TABLE workspace_role_definitions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    base_role    text        NOT NULL CHECK (base_role IN ('owner', 'admin', 'member', 'guest')),
    permissions  text[]      NOT NULL DEFAULT '{}',
    system       boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE workspace_role_definitions
    ADD CONSTRAINT workspace_role_definitions_ws_name_unique UNIQUE (workspace_id, name);

CREATE INDEX idx_workspace_role_definitions_workspace ON workspace_role_definitions (workspace_id);

CREATE TRIGGER trg_workspace_role_definitions_updated_at
    BEFORE UPDATE ON workspace_role_definitions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE workspace_role_assignments (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id      uuid        NOT NULL REFERENCES workspace_role_definitions (id) ON DELETE CASCADE,
    assigned_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    assigned_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE workspace_role_assignments
    ADD CONSTRAINT workspace_role_assignments_ws_user_role_unique UNIQUE (workspace_id, user_id, role_id);

CREATE INDEX idx_workspace_role_assignments_user ON workspace_role_assignments (workspace_id, user_id);

CREATE TABLE workspace_connections (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    source_workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    target_workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    status              text        NOT NULL DEFAULT 'pending'
                                      CHECK (status IN ('pending', 'active', 'paused', 'revoked')),
    policy              jsonb       NOT NULL DEFAULT '{}',
    created_by          uuid        REFERENCES users (id) ON DELETE SET NULL,
    approved_by         uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (source_workspace_id <> target_workspace_id)
);

ALTER TABLE workspace_connections
    ADD CONSTRAINT workspace_connections_pair_unique UNIQUE (source_workspace_id, target_workspace_id);

CREATE INDEX idx_workspace_connections_source ON workspace_connections (source_workspace_id, status);
CREATE INDEX idx_workspace_connections_target ON workspace_connections (target_workspace_id, status);

CREATE TRIGGER trg_workspace_connections_updated_at
    BEFORE UPDATE ON workspace_connections
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMIT;
