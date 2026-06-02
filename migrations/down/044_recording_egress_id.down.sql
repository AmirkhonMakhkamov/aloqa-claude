DROP INDEX IF EXISTS ux_recordings_active_per_call;
DROP INDEX IF EXISTS ux_recordings_egress_id;
ALTER TABLE recordings DROP COLUMN IF EXISTS egress_id;
