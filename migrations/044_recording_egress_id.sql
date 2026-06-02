-- ALK-701: correlate recordings with their LiveKit Egress job.
-- The egress_id is returned synchronously by StartRoomCompositeEgress and is the
-- primary key used by the egress_* webhook bridge to finalize the recording.
ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS egress_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS ux_recordings_egress_id
    ON recordings (egress_id)
    WHERE egress_id IS NOT NULL;

-- Durable single-active-recording invariant: at most one in-flight
-- (recording|processing) recording per call. Makes the StartRecording
-- conflict check race-proof (concurrent starts → unique violation → 409)
-- and keeps the egress webhook GetActiveByCall fallback unambiguous (ALK-701).
CREATE UNIQUE INDEX IF NOT EXISTS ux_recordings_active_per_call
    ON recordings (call_id)
    WHERE status IN ('recording', 'processing');
