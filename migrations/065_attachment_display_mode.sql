-- Per-attachment render hint so the sender can choose whether an image is shown
-- as an inline photo or as a download card (Telegram-style, ALK-926). Empty
-- string means "auto": fall back to the MIME-type heuristic, which preserves the
-- existing rendering for every row created before this column.
ALTER TABLE attachments
    ADD COLUMN IF NOT EXISTS display_mode text NOT NULL DEFAULT ''
        CHECK (display_mode IN ('', 'photo', 'file'));
