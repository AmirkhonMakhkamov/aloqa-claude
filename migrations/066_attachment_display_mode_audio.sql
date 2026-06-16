-- Extend the attachment display_mode hint to allow 'audio' so voice notes can
-- be stored with an explicit render signal (waveform player), mirroring how
-- 'photo' marks inline images (ALK-926). Existing rows are unaffected.
ALTER TABLE attachments
    DROP CONSTRAINT IF EXISTS attachments_display_mode_check;

ALTER TABLE attachments
    ADD CONSTRAINT attachments_display_mode_check
        CHECK (display_mode IN ('', 'photo', 'file', 'audio'));
