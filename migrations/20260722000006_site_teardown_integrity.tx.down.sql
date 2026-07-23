DROP TRIGGER IF EXISTS sites_require_no_backup_plan_targets;
--bun:split
DROP TRIGGER IF EXISTS backup_plan_targets_update;
--bun:split
DROP TRIGGER IF EXISTS backup_plan_targets_insert;
--bun:split
DROP TABLE IF EXISTS backup_plan_databases;
--bun:split
DROP TABLE IF EXISTS backup_plan_sites;
--bun:split
CREATE TABLE scheduled_task_plans_retained AS SELECT * FROM scheduled_task_plans;
--bun:split
CREATE TABLE scheduled_tasks_unconstrained (
	id TEXT PRIMARY KEY,
	site_id TEXT NOT NULL,
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
INSERT INTO scheduled_tasks_unconstrained
	(id, site_id, name, cron_expression, command, timeout_seconds, enabled, status, pending_removal, last_job_id, failure, created_at, updated_at)
SELECT id, site_id, name, cron_expression, command, timeout_seconds, enabled, status, pending_removal, last_job_id, failure, created_at, updated_at
FROM scheduled_tasks;
--bun:split
DROP TABLE scheduled_tasks;
--bun:split
ALTER TABLE scheduled_tasks_unconstrained RENAME TO scheduled_tasks;
--bun:split
CREATE INDEX scheduled_tasks_site_idx ON scheduled_tasks (site_id, name);
--bun:split
INSERT INTO scheduled_task_plans (task_id, plan_json, created_at, expires_at)
SELECT task_id, plan_json, created_at, expires_at FROM scheduled_task_plans_retained;
--bun:split
DROP TABLE scheduled_task_plans_retained;
--bun:split
CREATE TABLE sftp_access_unconstrained (
	site_id TEXT PRIMARY KEY,
	enabled BOOLEAN NOT NULL DEFAULT 0,
	username TEXT NOT NULL,
	password_set_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
INSERT INTO sftp_access_unconstrained (site_id, enabled, username, password_set_at, updated_at)
SELECT site_id, enabled, username, password_set_at, updated_at FROM sftp_access;
--bun:split
DROP TABLE sftp_access;
--bun:split
ALTER TABLE sftp_access_unconstrained RENAME TO sftp_access;
