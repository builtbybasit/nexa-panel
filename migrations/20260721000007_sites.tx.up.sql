CREATE TABLE sites (
	id TEXT PRIMARY KEY,
	slug TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	primary_domain TEXT NOT NULL UNIQUE,
	php_version TEXT NOT NULL,
	unix_user TEXT NOT NULL UNIQUE,
	root_path TEXT NOT NULL UNIQUE,
	socket_path TEXT NOT NULL UNIQUE,
	status TEXT NOT NULL,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX sites_status_slug_idx ON sites (status, slug);
--bun:split
CREATE TABLE site_plans (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	plan_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
