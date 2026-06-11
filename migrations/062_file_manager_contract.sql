-- File Manager contract mirror (ALK-889).

ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS file_ids uuid[] NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS library_files (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    filename     text        NOT NULL,
    extension    text        NOT NULL,
    mime_type    text        NOT NULL,
    size         bigint      NOT NULL CHECK (size > 0),
    storage_path text        NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);

CREATE INDEX IF NOT EXISTS idx_library_files_workspace_created
    ON library_files (workspace_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_library_files_owner_usage
    ON library_files (user_id, deleted_at)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_library_files_name
    ON library_files (workspace_id, lower(filename), created_at, id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_library_files_size
    ON library_files (workspace_id, size DESC, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS library_file_shares (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id      uuid        NOT NULL REFERENCES library_files (id) ON DELETE CASCADE,
    target_type  text        NOT NULL CHECK (target_type IN ('channel', 'user')),
    target_id    uuid        NOT NULL,
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (file_id, target_type, target_id)
);

CREATE INDEX IF NOT EXISTS idx_library_file_shares_target
    ON library_file_shares (workspace_id, target_type, target_id, file_id);

CREATE TABLE IF NOT EXISTS file_favorites (
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    file_id    uuid        NOT NULL REFERENCES library_files (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, file_id)
);

CREATE INDEX IF NOT EXISTS idx_file_favorites_file
    ON file_favorites (file_id);
