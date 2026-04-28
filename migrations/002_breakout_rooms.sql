-- Migration: 002_breakout_rooms
-- Adds breakout room support for calls.

BEGIN;

-- ============================================================
-- 1. breakout_rooms
-- ============================================================
CREATE TABLE breakout_rooms (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    call_id     uuid        NOT NULL REFERENCES calls (id) ON DELETE CASCADE,
    name        text        NOT NULL,
    created_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    time_limit  int,        -- seconds; NULL = no limit
    status      text        NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'closed')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    closed_at   timestamptz
);

CREATE INDEX idx_breakout_rooms_call_id ON breakout_rooms (call_id);
CREATE INDEX idx_breakout_rooms_call_active ON breakout_rooms (call_id)
    WHERE status = 'active';

-- ============================================================
-- 2. Add breakout_room_id to call_participants
-- ============================================================
ALTER TABLE call_participants
    ADD COLUMN breakout_room_id uuid REFERENCES breakout_rooms (id) ON DELETE SET NULL;

CREATE INDEX idx_call_participants_breakout ON call_participants (breakout_room_id)
    WHERE breakout_room_id IS NOT NULL;

COMMIT;
