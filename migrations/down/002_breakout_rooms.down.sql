BEGIN;
DROP INDEX IF EXISTS idx_call_participants_breakout;
ALTER TABLE call_participants DROP COLUMN IF EXISTS breakout_room_id;
DROP INDEX IF EXISTS idx_breakout_rooms_call_active;
DROP INDEX IF EXISTS idx_breakout_rooms_call_id;
DROP TABLE IF EXISTS breakout_rooms;
COMMIT;
