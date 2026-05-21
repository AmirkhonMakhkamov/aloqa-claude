BEGIN;

-- Add free-form position (job title) and department to the users table.
-- Both columns are nullable so existing rows remain untouched and clients
-- that don't supply the fields keep working unchanged.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS position   TEXT,
    ADD COLUMN IF NOT EXISTS department TEXT;

-- Backfill the deterministic test users used by scripts/seed.sh and the
-- staging deploy (see CLAUDE memory release_0_11_0 / vultr_staging_deploy).
-- The WHERE clause is idempotent: it only writes when the column is still
-- NULL, so re-running the migration does not stomp on manually edited rows.
UPDATE users
   SET position   = 'Director of Product, Collaboration Tools',
       department = 'Product'
 WHERE email ILIKE 'alice@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Senior Software Engineer, Platform Infrastructure',
       department = 'Engineering'
 WHERE email ILIKE 'bob@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Principal Product Designer, Communication',
       department = 'Design'
 WHERE email ILIKE 'carol@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Engineering Manager, Realtime',
       department = 'Engineering'
 WHERE email ILIKE 'david@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Head of People & Culture',
       department = 'People & Culture'
 WHERE email ILIKE 'eve@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Finance Business Partner, R&D',
       department = 'Finance'
 WHERE email ILIKE 'frank@aloqa.test'
   AND (position IS NULL OR department IS NULL);

-- Best-effort coverage of other staging accounts referenced in CLAUDE
-- memory (release_0_11_0_2026_05_21 + vultr_staging_deploy). These rows
-- may not exist on every environment; UPDATE on a missing row is a no-op.
UPDATE users
   SET position   = 'Staff Frontend Engineer, Design Systems',
       department = 'Engineering'
 WHERE email ILIKE 'admin@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Senior Recruiter, Engineering & Design',
       department = 'People & Culture'
 WHERE email ILIKE 'recruiter@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Compensation & Benefits Lead',
       department = 'People & Culture'
 WHERE email ILIKE 'comp@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Head of Revenue Operations',
       department = 'Operations'
 WHERE email ILIKE 'revops@aloqa.test'
   AND (position IS NULL OR department IS NULL);

UPDATE users
   SET position   = 'Senior Customer Success Manager, Strategic Accounts',
       department = 'Customer Success'
 WHERE email ILIKE 'csm@aloqa.test'
   AND (position IS NULL OR department IS NULL);

COMMIT;
