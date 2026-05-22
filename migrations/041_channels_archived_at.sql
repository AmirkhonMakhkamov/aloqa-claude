BEGIN;

-- ALK-617: surface a list view of archived channels with the timestamp at
-- which the channel was archived. Nullable so unarchived rows stay NULL and
-- pre-existing archived rows are backfilled to `updated_at` (best available
-- approximation since older archive operations did not record a dedicated
-- timestamp).
ALTER TABLE channels
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

UPDATE channels
   SET archived_at = updated_at
 WHERE archived = TRUE
   AND archived_at IS NULL;

COMMIT;
