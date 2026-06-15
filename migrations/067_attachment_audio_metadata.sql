-- Server-computed audio metadata for voice notes so clients never have to
-- decode the whole file to learn its length or draw a waveform. Both nullable:
-- existing rows and non-audio attachments stay NULL, and the client falls back
-- to client-side decoding when they are absent.
ALTER TABLE attachments
    ADD COLUMN IF NOT EXISTS duration_ms    integer,
    ADD COLUMN IF NOT EXISTS waveform_peaks real[];
