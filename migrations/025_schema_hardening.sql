-- Migration: 025_schema_hardening
-- Adds CHECK constraints on numeric ranges, recording status state machine,
-- and missing enum constraints.

BEGIN;

-- ============================================================
-- 1. Numeric range constraints: guest_invites
-- ============================================================
ALTER TABLE guest_invites
    ADD CONSTRAINT guest_invites_max_uses_positive CHECK (max_uses >= 1),
    ADD CONSTRAINT guest_invites_use_count_nonneg  CHECK (use_count >= 0),
    ADD CONSTRAINT guest_invites_use_count_bounded  CHECK (use_count <= max_uses);

-- ============================================================
-- 2. Numeric range constraints: search_index_jobs
-- ============================================================
ALTER TABLE search_index_jobs
    ADD CONSTRAINT search_index_jobs_attempts_nonneg     CHECK (attempts >= 0),
    ADD CONSTRAINT search_index_jobs_max_attempts_pos    CHECK (max_attempts >= 1),
    ADD CONSTRAINT search_index_jobs_attempts_bounded    CHECK (attempts <= max_attempts);

-- ============================================================
-- 3. Numeric range constraints: recordings
-- ============================================================
ALTER TABLE recordings
    ADD CONSTRAINT recordings_duration_nonneg            CHECK (duration IS NULL OR duration >= 0),
    ADD CONSTRAINT recordings_file_size_nonneg            CHECK (file_size IS NULL OR file_size >= 0),
    ADD CONSTRAINT recordings_processing_attempts_nonneg  CHECK (processing_attempts >= 0),
    ADD CONSTRAINT recordings_max_processing_attempts_pos CHECK (max_processing_attempts >= 1),
    ADD CONSTRAINT recordings_attempts_bounded            CHECK (processing_attempts <= max_processing_attempts);

-- ============================================================
-- 4. Numeric range constraints: recording_artifacts
-- ============================================================
ALTER TABLE recording_artifacts
    ADD CONSTRAINT recording_artifacts_file_size_nonneg     CHECK (file_size >= 0),
    ADD CONSTRAINT recording_artifacts_packet_count_nonneg  CHECK (packet_count >= 0),
    ADD CONSTRAINT recording_artifacts_duration_nonneg      CHECK (duration IS NULL OR duration >= 0);

-- ============================================================
-- 5. Numeric range constraints: attachments
-- ============================================================
ALTER TABLE attachments
    ADD CONSTRAINT attachments_file_size_positive CHECK (file_size > 0);

-- ============================================================
-- 6. Numeric range constraints: breakout_rooms
-- ============================================================
ALTER TABLE breakout_rooms
    ADD CONSTRAINT breakout_rooms_time_limit_nonneg CHECK (time_limit IS NULL OR time_limit >= 0);

-- ============================================================
-- 7. Numeric range constraints: media_qos_samples
-- ============================================================
ALTER TABLE media_qos_samples
    ADD CONSTRAINT media_qos_packet_loss_range   CHECK (packet_loss_pct >= 0 AND packet_loss_pct <= 100),
    ADD CONSTRAINT media_qos_jitter_nonneg       CHECK (jitter_ms >= 0),
    ADD CONSTRAINT media_qos_rtt_nonneg          CHECK (round_trip_time_ms >= 0),
    ADD CONSTRAINT media_qos_bitrate_out_nonneg  CHECK (available_outgoing_bitrate_kbps >= 0),
    ADD CONSTRAINT media_qos_bitrate_in_nonneg   CHECK (available_incoming_bitrate_kbps >= 0),
    ADD CONSTRAINT media_qos_bytes_sent_nonneg   CHECK (bytes_sent >= 0),
    ADD CONSTRAINT media_qos_bytes_recv_nonneg   CHECK (bytes_received >= 0);

-- ============================================================
-- 8. Numeric range constraints: media_quality_policies
-- ============================================================
ALTER TABLE media_quality_policies
    ADD CONSTRAINT mqp_alert_loss_range          CHECK (alert_packet_loss_pct >= 0 AND alert_packet_loss_pct <= 100),
    ADD CONSTRAINT mqp_alert_jitter_nonneg       CHECK (alert_jitter_ms >= 0),
    ADD CONSTRAINT mqp_alert_rtt_nonneg          CHECK (alert_round_trip_time_ms >= 0),
    ADD CONSTRAINT mqp_corr_tolerance_pct_range  CHECK (correlation_tolerance_pct >= 0 AND correlation_tolerance_pct <= 100),
    ADD CONSTRAINT mqp_corr_tolerance_ms_nonneg  CHECK (correlation_tolerance_ms >= 0);

-- ============================================================
-- 9. Numeric range constraints: media_room_placements
-- ============================================================
ALTER TABLE media_room_placements
    ADD CONSTRAINT mrp_max_participants_nonneg CHECK (max_participants >= 0),
    ADD CONSTRAINT mrp_max_presenters_nonneg   CHECK (max_presenters >= 0),
    ADD CONSTRAINT mrp_max_viewers_nonneg      CHECK (max_viewers >= 0);

-- ============================================================
-- 10. Numeric range constraints: media_relay_edges
-- ============================================================
ALTER TABLE media_relay_edges
    ADD CONSTRAINT mre_max_participants_nonneg CHECK (max_participants >= 0),
    ADD CONSTRAINT mre_priority_nonneg         CHECK (priority >= 0);

-- ============================================================
-- 11. Missing enum constraints: media_quality_alerts
-- ============================================================
ALTER TABLE media_quality_alerts
    ADD CONSTRAINT media_quality_alerts_kind_check
        CHECK (kind IN ('packet_loss', 'jitter', 'rtt', 'bitrate', 'degraded_overall', 'mismatch')),
    ADD CONSTRAINT media_quality_alerts_severity_check
        CHECK (severity IN ('warning', 'critical')),
    ADD CONSTRAINT media_quality_alerts_status_check
        CHECK (status IN ('active', 'resolved', 'acknowledged'));

-- ============================================================
-- 12. Recording status state machine trigger
-- Enforces valid transitions:
--   recording  -> processing, failed
--   processing -> ready, failed
--   failed     -> processing  (retry)
-- ============================================================
CREATE OR REPLACE FUNCTION enforce_recording_status_transition()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status = NEW.status THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'recording' AND NEW.status IN ('processing', 'failed') THEN
        RETURN NEW;
    ELSIF OLD.status = 'processing' AND NEW.status IN ('ready', 'failed') THEN
        RETURN NEW;
    ELSIF OLD.status = 'failed' AND NEW.status = 'processing' THEN
        RETURN NEW;
    ELSE
        RAISE EXCEPTION 'invalid recording status transition: % -> %', OLD.status, NEW.status;
    END IF;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_recording_status_transition
    BEFORE UPDATE OF status ON recordings
    FOR EACH ROW
    EXECUTE FUNCTION enforce_recording_status_transition();

COMMIT;
