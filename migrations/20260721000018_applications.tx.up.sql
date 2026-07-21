CREATE TABLE applications (
	id TEXT PRIMARY KEY,
	app TEXT NOT NULL,
	version TEXT NOT NULL,
	status TEXT NOT NULL,
	installed_version TEXT,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE TABLE application_plans (
	id TEXT PRIMARY KEY,
	application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
	operation TEXT NOT NULL,
	plan_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX application_plans_app_idx ON application_plans(application_id, created_at DESC);
