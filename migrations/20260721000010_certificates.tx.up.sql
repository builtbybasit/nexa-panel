CREATE TABLE certificates (
	id TEXT PRIMARY KEY,
	site_id TEXT NOT NULL UNIQUE REFERENCES sites(id) ON DELETE CASCADE,
	primary_domain TEXT NOT NULL,
	email TEXT NOT NULL,
	status TEXT NOT NULL,
	domains_json TEXT NOT NULL DEFAULT '[]',
	certificate_path TEXT,
	private_key_path TEXT,
	issued_at TIMESTAMP,
	expires_at TIMESTAMP,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE TABLE certificate_plans (
	certificate_id TEXT PRIMARY KEY REFERENCES certificates(id) ON DELETE CASCADE,
	operation TEXT NOT NULL,
	plan_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
