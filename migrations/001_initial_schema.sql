-- Migration: 001_initial_schema
-- Aloqa Corporate Communication Platform
-- PostgreSQL 15+

BEGIN;

-- ============================================================
-- Trigger function: auto-update updated_at on row modification
-- ============================================================
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ============================================================
-- 1. users
-- ============================================================
CREATE TABLE users (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email       text        NOT NULL,
    display_name text       NOT NULL,
    avatar_url  text,
    password_hash text      NOT NULL,
    status      text        NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'suspended', 'deactivated')),
    locale      text        NOT NULL DEFAULT 'uz',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE users ADD CONSTRAINT users_email_unique UNIQUE (email);

CREATE INDEX idx_users_email  ON users (email);
CREATE INDEX idx_users_status ON users (status);

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ============================================================
-- 2. workspaces
-- ============================================================
CREATE TABLE workspaces (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    slug        text        NOT NULL,
    avatar_url  text,
    created_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE workspaces ADD CONSTRAINT workspaces_slug_unique UNIQUE (slug);

CREATE INDEX idx_workspaces_created_by ON workspaces (created_by);

CREATE TRIGGER trg_workspaces_updated_at
    BEFORE UPDATE ON workspaces
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ============================================================
-- 3. workspace_members
-- ============================================================
CREATE TABLE workspace_members (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         text        NOT NULL DEFAULT 'member'
                             CHECK (role IN ('owner', 'admin', 'member', 'guest')),
    joined_at    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE workspace_members
    ADD CONSTRAINT workspace_members_ws_user_unique UNIQUE (workspace_id, user_id);

CREATE INDEX idx_workspace_members_user_id      ON workspace_members (user_id);
CREATE INDEX idx_workspace_members_workspace_id ON workspace_members (workspace_id);

-- ============================================================
-- 4. channels
-- ============================================================
CREATE TABLE channels (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    topic        text,
    type         text        NOT NULL DEFAULT 'public'
                             CHECK (type IN ('public', 'private', 'dm', 'group_dm')),
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    archived     boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_channels_workspace_id ON channels (workspace_id);
CREATE INDEX idx_channels_created_by   ON channels (created_by);
CREATE INDEX idx_channels_workspace_type ON channels (workspace_id, type)
    WHERE archived = false;

CREATE TRIGGER trg_channels_updated_at
    BEFORE UPDATE ON channels
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ============================================================
-- 5. channel_members
-- ============================================================
CREATE TABLE channel_members (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id   uuid        NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         text        NOT NULL DEFAULT 'member'
                             CHECK (role IN ('owner', 'admin', 'member')),
    muted_until  timestamptz,
    last_read_at timestamptz NOT NULL DEFAULT now(),
    joined_at    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE channel_members
    ADD CONSTRAINT channel_members_ch_user_unique UNIQUE (channel_id, user_id);

CREATE INDEX idx_channel_members_user_id    ON channel_members (user_id);
CREATE INDEX idx_channel_members_channel_id ON channel_members (channel_id);

-- ============================================================
-- 6. messages
-- ============================================================
CREATE TABLE messages (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id uuid        NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE SET NULL,
    parent_id  uuid        REFERENCES messages (id) ON DELETE SET NULL,
    content    text        NOT NULL,
    type       text        NOT NULL DEFAULT 'text'
                           CHECK (type IN ('text', 'system', 'file')),
    edited     boolean     NOT NULL DEFAULT false,
    edited_at  timestamptz,
    pinned     boolean     NOT NULL DEFAULT false,
    pinned_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    pinned_at  timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

-- Primary listing query: messages in a channel ordered by time
CREATE INDEX idx_messages_channel_created ON messages (channel_id, created_at);

-- Thread queries: find replies to a parent message
CREATE INDEX idx_messages_parent_id ON messages (parent_id)
    WHERE parent_id IS NOT NULL;

-- Soft-delete filter
CREATE INDEX idx_messages_channel_active ON messages (channel_id, created_at)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_messages_user_id   ON messages (user_id);
CREATE INDEX idx_messages_pinned_by ON messages (pinned_by)
    WHERE pinned_by IS NOT NULL;

CREATE TRIGGER trg_messages_updated_at
    BEFORE UPDATE ON messages
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ============================================================
-- 7. reactions
-- ============================================================
CREATE TABLE reactions (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id uuid        NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    emoji      varchar(32) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE reactions
    ADD CONSTRAINT reactions_msg_user_emoji_unique UNIQUE (message_id, user_id, emoji);

CREATE INDEX idx_reactions_message_id ON reactions (message_id);
CREATE INDEX idx_reactions_user_id    ON reactions (user_id);

-- ============================================================
-- 8. attachments
-- ============================================================
CREATE TABLE attachments (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id   uuid        NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    file_name    text        NOT NULL,
    file_size    bigint      NOT NULL,
    mime_type    text        NOT NULL,
    storage_path text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_attachments_message_id ON attachments (message_id);

-- ============================================================
-- 9. calls
-- ============================================================
CREATE TABLE calls (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    channel_id   uuid        REFERENCES channels (id) ON DELETE SET NULL,
    type         text        NOT NULL
                             CHECK (type IN ('one_to_one', 'group', 'meeting', 'webinar', 'selector')),
    status       text        NOT NULL DEFAULT 'ringing'
                             CHECK (status IN ('ringing', 'active', 'ended')),
    title        text,
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    settings     jsonb       NOT NULL DEFAULT '{}',
    started_at   timestamptz,
    ended_at     timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_calls_workspace_id ON calls (workspace_id);
CREATE INDEX idx_calls_channel_id   ON calls (channel_id)
    WHERE channel_id IS NOT NULL;
CREATE INDEX idx_calls_created_by   ON calls (created_by);

-- Fast lookup for active/ringing calls in a workspace
CREATE INDEX idx_calls_workspace_active ON calls (workspace_id)
    WHERE status != 'ended';

-- ============================================================
-- 10. call_participants
-- ============================================================
CREATE TABLE call_participants (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    call_id         uuid        NOT NULL REFERENCES calls (id) ON DELETE CASCADE,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role            text        NOT NULL DEFAULT 'participant'
                                CHECK (role IN ('host', 'co_host', 'presenter', 'participant', 'viewer')),
    status          text        NOT NULL DEFAULT 'invited'
                                CHECK (status IN ('invited', 'joining', 'connected', 'disconnected')),
    audio_muted     boolean     NOT NULL DEFAULT false,
    video_muted     boolean     NOT NULL DEFAULT false,
    screen_sharing  boolean     NOT NULL DEFAULT false,
    joined_at       timestamptz,
    left_at         timestamptz
);

ALTER TABLE call_participants
    ADD CONSTRAINT call_participants_call_user_unique UNIQUE (call_id, user_id);

CREATE INDEX idx_call_participants_call_id ON call_participants (call_id);
CREATE INDEX idx_call_participants_user_id ON call_participants (user_id);

-- Active participants in a call
CREATE INDEX idx_call_participants_active ON call_participants (call_id)
    WHERE status IN ('joining', 'connected');

COMMIT;
