-- Relational integrity for everything a site teardown has to reason about.
-- Until now three of the site's dependents were joined to it by convention
-- only: sftp_access.site_id and scheduled_tasks.site_id were plain columns, and
-- a backup plan's targets were JSON blobs no query could join against, so
-- checking whether a plan referenced a site meant scanning every plan row.
CREATE TABLE sftp_access_rebuilt (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	enabled BOOLEAN NOT NULL DEFAULT 0,
	username TEXT NOT NULL,
	password_set_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
-- Rows naming a site that no longer exists are exactly the orphans this
-- migration exists to make impossible, so they are not carried over.
INSERT INTO sftp_access_rebuilt (site_id, enabled, username, password_set_at, updated_at)
SELECT site_id, enabled, username, password_set_at, updated_at
FROM sftp_access WHERE site_id IN (SELECT id FROM sites);
--bun:split
DROP TABLE sftp_access;
--bun:split
ALTER TABLE sftp_access_rebuilt RENAME TO sftp_access;
--bun:split
-- scheduled_tasks is a parent of scheduled_task_plans, and with foreign keys
-- enabled DROP TABLE performs an implicit DELETE that would cascade those plans
-- away. They are parked in a holding table and restored once the rebuilt parent
-- is back under its own name.
CREATE TABLE scheduled_task_plans_retained AS SELECT * FROM scheduled_task_plans;
--bun:split
CREATE TABLE scheduled_tasks_rebuilt (
	id TEXT PRIMARY KEY,
	site_id TEXT NOT NULL REFERENCES sites(id),
	name TEXT NOT NULL,
	cron_expression TEXT NOT NULL,
	command TEXT NOT NULL,
	timeout_seconds INTEGER NOT NULL,
	enabled INTEGER NOT NULL,
	status TEXT NOT NULL,
	pending_removal INTEGER NOT NULL DEFAULT 0,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
INSERT INTO scheduled_tasks_rebuilt
	(id, site_id, name, cron_expression, command, timeout_seconds, enabled, status, pending_removal, last_job_id, failure, created_at, updated_at)
SELECT id, site_id, name, cron_expression, command, timeout_seconds, enabled, status, pending_removal, last_job_id, failure, created_at, updated_at
FROM scheduled_tasks WHERE site_id IN (SELECT id FROM sites);
--bun:split
DROP TABLE scheduled_tasks;
--bun:split
ALTER TABLE scheduled_tasks_rebuilt RENAME TO scheduled_tasks;
--bun:split
CREATE INDEX scheduled_tasks_site_idx ON scheduled_tasks (site_id, name);
--bun:split
INSERT INTO scheduled_task_plans (task_id, plan_json, created_at, expires_at)
SELECT task_id, plan_json, created_at, expires_at FROM scheduled_task_plans_retained
WHERE task_id IN (SELECT id FROM scheduled_tasks);
--bun:split
DROP TABLE scheduled_task_plans_retained;
--bun:split
-- The join tables make a backup plan's targets queryable. They are derived
-- from the JSON columns by trigger rather than written by the application, so
-- they cannot drift out of step with the plan they describe. site_id carries no
-- foreign key on purpose: a plan may legitimately outlive a target that was
-- removed out of band, and refusing the plan write would be worse than
-- reporting the stale reference.
CREATE TABLE backup_plan_sites (
	plan_id TEXT NOT NULL REFERENCES backup_plans(id) ON DELETE CASCADE,
	site_id TEXT NOT NULL,
	PRIMARY KEY (plan_id, site_id)
);
--bun:split
CREATE INDEX backup_plan_sites_site_idx ON backup_plan_sites (site_id);
--bun:split
CREATE TABLE backup_plan_databases (
	plan_id TEXT NOT NULL REFERENCES backup_plans(id) ON DELETE CASCADE,
	database_id TEXT NOT NULL,
	PRIMARY KEY (plan_id, database_id)
);
--bun:split
CREATE INDEX backup_plan_databases_database_idx ON backup_plan_databases (database_id);
--bun:split
INSERT OR IGNORE INTO backup_plan_sites (plan_id, site_id)
SELECT backup_plans.id, target.value FROM backup_plans, json_each(backup_plans.site_ids) AS target;
--bun:split
INSERT OR IGNORE INTO backup_plan_databases (plan_id, database_id)
SELECT backup_plans.id, target.value FROM backup_plans, json_each(backup_plans.database_ids) AS target;
--bun:split
CREATE TRIGGER backup_plan_targets_insert
AFTER INSERT ON backup_plans
BEGIN
	INSERT OR IGNORE INTO backup_plan_sites (plan_id, site_id)
		SELECT NEW.id, target.value FROM json_each(NEW.site_ids) AS target;
	INSERT OR IGNORE INTO backup_plan_databases (plan_id, database_id)
		SELECT NEW.id, target.value FROM json_each(NEW.database_ids) AS target;
END;
--bun:split
CREATE TRIGGER backup_plan_targets_update
AFTER UPDATE OF site_ids, database_ids ON backup_plans
BEGIN
	DELETE FROM backup_plan_sites WHERE plan_id = NEW.id;
	DELETE FROM backup_plan_databases WHERE plan_id = NEW.id;
	INSERT OR IGNORE INTO backup_plan_sites (plan_id, site_id)
		SELECT NEW.id, target.value FROM json_each(NEW.site_ids) AS target;
	INSERT OR IGNORE INTO backup_plan_databases (plan_id, database_id)
		SELECT NEW.id, target.value FROM json_each(NEW.database_ids) AS target;
END;
--bun:split
-- The last-resort guard, mirroring backup_plans_require_no_copies: the module
-- refuses the deletion long before this, but a site row must never be able to
-- disappear from underneath a plan that still names it.
CREATE TRIGGER sites_require_no_backup_plan_targets
BEFORE DELETE ON sites
FOR EACH ROW WHEN EXISTS (SELECT 1 FROM backup_plan_sites WHERE site_id = OLD.id)
BEGIN
	SELECT RAISE(ABORT, 'site is still targeted by a backup plan');
END;
