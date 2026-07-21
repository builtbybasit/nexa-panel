CREATE TABLE scheduled_tasks (
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
CREATE INDEX scheduled_tasks_site_idx ON scheduled_tasks (site_id, name);
--bun:split
CREATE TABLE scheduled_task_plans (
	task_id TEXT PRIMARY KEY REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
	plan_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
