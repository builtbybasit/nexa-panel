-- The last convention-only edge in the site ownership graph. A site's databases
-- were joined to it by name: teardown asked which managed databases were called
-- "<unix_user>" or "<unix_user>_*" and blocked on the answer, so a database
-- created with any other name was invisible to the check and the site could be
-- deleted out from under it. site_id makes the relation real and queryable.
ALTER TABLE managed_databases ADD COLUMN site_id TEXT REFERENCES sites(id);
--bun:split
ALTER TABLE mysql_databases ADD COLUMN site_id TEXT REFERENCES sites(id);
--bun:split
CREATE INDEX managed_databases_site_idx ON managed_databases (site_id);
--bun:split
CREATE INDEX mysql_databases_site_idx ON mysql_databases (site_id);
--bun:split
-- Backfill from the convention the column replaces, so databases created before
-- the relation existed keep the association teardown already assumed. The
-- longest matching account wins: with sites "nexa_app" and "nexa_app_web", a
-- database named "nexa_app_web_main" belongs to the more specific one.
UPDATE managed_databases SET site_id = (
	SELECT site.id FROM sites AS site
	WHERE managed_databases.name = site.unix_user
		OR substr(managed_databases.name, 1, length(site.unix_user) + 1) = site.unix_user || '_'
	ORDER BY length(site.unix_user) DESC LIMIT 1
);
--bun:split
UPDATE mysql_databases SET site_id = (
	SELECT site.id FROM sites AS site
	WHERE mysql_databases.name = site.unix_user
		OR substr(mysql_databases.name, 1, length(site.unix_user) + 1) = site.unix_user || '_'
	ORDER BY length(site.unix_user) DESC LIMIT 1
);
--bun:split
-- The column deliberately does not cascade or null itself out. A database is a
-- data store, not a rendered artifact: dropping the site's row must never
-- silently detach one whose owning account and files are about to be removed.
-- The foreign key already restricts the delete; these triggers mirror
-- sites_require_no_backup_plan_targets so the refusal says which relation held
-- it, matching the message dependentBlocker gives the operator beforehand.
CREATE TRIGGER sites_require_no_owned_postgresql_databases
BEFORE DELETE ON sites
FOR EACH ROW WHEN EXISTS (SELECT 1 FROM managed_databases WHERE site_id = OLD.id)
BEGIN
	SELECT RAISE(ABORT, 'site still owns a managed PostgreSQL database');
END;
--bun:split
CREATE TRIGGER sites_require_no_owned_mysql_databases
BEFORE DELETE ON sites
FOR EACH ROW WHEN EXISTS (SELECT 1 FROM mysql_databases WHERE site_id = OLD.id)
BEGIN
	SELECT RAISE(ABORT, 'site still owns a managed MySQL database');
END;
