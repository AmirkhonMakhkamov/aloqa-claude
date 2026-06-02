-- Revert 049: call-scoped guest access grant column + index.
DROP INDEX IF EXISTS idx_guest_access_grants_call_id;
ALTER TABLE guest_access_grants DROP COLUMN IF EXISTS call_id;
