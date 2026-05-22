BEGIN;

-- Add nullable phone and timezone to the users table. Both columns are
-- nullable so existing rows keep working and clients that omit the fields
-- continue to round-trip unchanged. `timezone` stores an IANA name
-- (e.g. "Asia/Tashkent", "Europe/Moscow", "America/Los_Angeles").
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS phone    TEXT,
    ADD COLUMN IF NOT EXISTS timezone TEXT;

-- Backfill the deterministic test users used by scripts/seed.sh and the
-- staging deploy (see CLAUDE memory release_0_11_0 / vultr_staging_deploy).
-- Idempotent: WHERE clause only writes when columns are still NULL so
-- manually edited rows are preserved on re-run.
UPDATE users
   SET phone    = '+998-90-123-45-67',
       timezone = 'Asia/Tashkent'
 WHERE email ILIKE 'alice@aloqa.test'
   AND (phone IS NULL OR timezone IS NULL);

UPDATE users
   SET phone    = '+1-415-555-0142',
       timezone = 'America/Los_Angeles'
 WHERE email ILIKE 'bob@aloqa.test'
   AND (phone IS NULL OR timezone IS NULL);

UPDATE users
   SET phone    = '+44-20-7946-0958',
       timezone = 'Europe/London'
 WHERE email ILIKE 'carol@aloqa.test'
   AND (phone IS NULL OR timezone IS NULL);

UPDATE users
   SET timezone = 'Europe/Moscow'
 WHERE email ILIKE 'david@aloqa.test'
   AND timezone IS NULL;

UPDATE users
   SET phone    = '+81-3-1234-5678',
       timezone = 'Asia/Tokyo'
 WHERE email ILIKE 'eve@aloqa.test'
   AND (phone IS NULL OR timezone IS NULL);

UPDATE users
   SET timezone = 'Asia/Almaty'
 WHERE email ILIKE 'frank@aloqa.test'
   AND timezone IS NULL;

-- Best-effort coverage of other staging accounts referenced in CLAUDE
-- memory; UPDATE on a missing row is a no-op.
UPDATE users
   SET phone    = '+998-77-111-22-33',
       timezone = 'Asia/Tashkent'
 WHERE email ILIKE 'admin@aloqa.test'
   AND (phone IS NULL OR timezone IS NULL);

UPDATE users
   SET timezone = 'Europe/Berlin'
 WHERE email ILIKE 'recruiter@aloqa.test'
   AND timezone IS NULL;

UPDATE users
   SET timezone = 'America/New_York'
 WHERE email ILIKE 'comp@aloqa.test'
   AND timezone IS NULL;

UPDATE users
   SET timezone = 'Asia/Singapore'
 WHERE email ILIKE 'revops@aloqa.test'
   AND timezone IS NULL;

UPDATE users
   SET timezone = 'Australia/Sydney'
 WHERE email ILIKE 'csm@aloqa.test'
   AND timezone IS NULL;

COMMIT;
