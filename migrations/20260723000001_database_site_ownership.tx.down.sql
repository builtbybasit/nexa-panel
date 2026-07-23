DROP TRIGGER IF EXISTS sites_require_no_owned_mysql_databases;
--bun:split
DROP TRIGGER IF EXISTS sites_require_no_owned_postgresql_databases;
--bun:split
-- SQLite refuses to drop a column an index is built on, so the indexes go first.
DROP INDEX IF EXISTS mysql_databases_site_idx;
--bun:split
DROP INDEX IF EXISTS managed_databases_site_idx;
--bun:split
ALTER TABLE mysql_databases DROP COLUMN site_id;
--bun:split
ALTER TABLE managed_databases DROP COLUMN site_id;
