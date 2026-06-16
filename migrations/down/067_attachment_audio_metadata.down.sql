ALTER TABLE attachments
    DROP COLUMN IF EXISTS duration_ms,
    DROP COLUMN IF EXISTS waveform_peaks;
