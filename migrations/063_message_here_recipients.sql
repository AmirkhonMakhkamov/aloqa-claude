-- Snapshot of members who were online when an @here broadcast mention was sent.
-- Presence is transient, so the recipient set is frozen at send time to keep the
-- mention feed consistent on reload (ALK broadcast mentions).
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS mention_here_recipients uuid[] NOT NULL DEFAULT '{}';
