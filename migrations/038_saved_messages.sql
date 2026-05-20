BEGIN;

-- ALK-394: Saved Messages self-chat support.
--
-- channels.type is a text column with a CHECK constraint in this repository,
-- not a PostgreSQL enum. Replace the existing type constraint with the
-- expanded saved-channel set.
DO $$
DECLARE
    constraint_name text;
BEGIN
    SELECT con.conname
      INTO constraint_name
      FROM pg_constraint con
      JOIN pg_attribute att
        ON att.attrelid = con.conrelid
       AND att.attnum = ANY (con.conkey)
     WHERE con.conrelid = 'channels'::regclass
       AND con.contype = 'c'
       AND att.attname = 'type'
     LIMIT 1;

    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE channels DROP CONSTRAINT %I', constraint_name);
    END IF;
END $$;

ALTER TABLE channels
    ADD CONSTRAINT channels_type_check
    CHECK (type IN ('public', 'private', 'dm', 'group_dm', 'saved', 'saved_global'));

-- saved_global rows are user-owned and workspace-NULL.
ALTER TABLE channels
    ALTER COLUMN workspace_id DROP NOT NULL;

ALTER TABLE channels
    ADD COLUMN IF NOT EXISTS owner_user_id uuid REFERENCES users (id) ON DELETE CASCADE;

-- Existing non-self channels can leave owner_user_id NULL. Future saved and
-- saved_global rows must populate it so lazy creation can be idempotent.
CREATE UNIQUE INDEX IF NOT EXISTS channels_owner_self_uidx
    ON channels (owner_user_id, type, COALESCE(workspace_id::text, ''))
    WHERE type IN ('saved', 'saved_global');

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS saved_messages_mode text
    NOT NULL DEFAULT 'per_workspace'
    CHECK (saved_messages_mode IN ('per_workspace', 'global'));

ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS saved_from jsonb;

CREATE INDEX IF NOT EXISTS idx_messages_saved_from_message_id
    ON messages ((saved_from->>'message_id'))
    WHERE saved_from IS NOT NULL AND deleted_at IS NULL;

COMMIT;
