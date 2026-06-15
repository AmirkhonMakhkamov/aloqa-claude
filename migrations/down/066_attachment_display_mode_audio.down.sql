-- Revert the display_mode hint back to '', 'photo', 'file'. Any 'audio' rows
-- must be migrated away before this runs or the constraint will fail to add.
ALTER TABLE attachments
    DROP CONSTRAINT IF EXISTS attachments_display_mode_check;

ALTER TABLE attachments
    ADD CONSTRAINT attachments_display_mode_check
        CHECK (display_mode IN ('', 'photo', 'file'));
