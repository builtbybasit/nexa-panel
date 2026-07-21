CREATE TABLE admin_tools (
	kind TEXT PRIMARY KEY,
	image TEXT NOT NULL,
	container_name TEXT NOT NULL UNIQUE,
	port INTEGER NOT NULL UNIQUE,
	memory_mb INTEGER NOT NULL,
	pids_limit INTEGER NOT NULL,
	status TEXT NOT NULL,
	systemd_unit TEXT NOT NULL UNIQUE,
	on_demand BOOLEAN NOT NULL,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE TABLE admin_tool_plans (
	id TEXT PRIMARY KEY,
	tool_kind TEXT NOT NULL REFERENCES admin_tools(kind) ON DELETE CASCADE,
	operation TEXT NOT NULL,
	plan_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX admin_tool_plans_tool_idx ON admin_tool_plans(tool_kind, created_at DESC);
