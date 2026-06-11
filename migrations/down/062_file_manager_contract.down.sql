DROP INDEX IF EXISTS idx_file_favorites_file;
DROP TABLE IF EXISTS file_favorites;

DROP INDEX IF EXISTS idx_library_file_shares_target;
DROP TABLE IF EXISTS library_file_shares;

DROP INDEX IF EXISTS idx_library_files_size;
DROP INDEX IF EXISTS idx_library_files_name;
DROP INDEX IF EXISTS idx_library_files_owner_usage;
DROP INDEX IF EXISTS idx_library_files_workspace_created;
DROP TABLE IF EXISTS library_files;

ALTER TABLE messages
    DROP COLUMN IF EXISTS file_ids;
