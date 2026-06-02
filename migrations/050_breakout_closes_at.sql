ALTER TABLE breakout_rooms ADD COLUMN closes_at timestamptz NULL;

CREATE INDEX IF NOT EXISTS idx_breakout_rooms_closes_at
    ON breakout_rooms (closes_at)
    WHERE status = 'active' AND closes_at IS NOT NULL;
