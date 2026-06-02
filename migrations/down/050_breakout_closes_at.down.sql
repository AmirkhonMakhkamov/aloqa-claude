BEGIN;

DROP INDEX IF EXISTS idx_breakout_rooms_closes_at;

ALTER TABLE breakout_rooms DROP COLUMN IF EXISTS closes_at;

COMMIT;
