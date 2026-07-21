CREATE TABLE domains (
	id TEXT PRIMARY KEY,
	site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	hostname TEXT NOT NULL UNIQUE,
	kind TEXT NOT NULL,
	redirect_target TEXT,
	status TEXT NOT NULL,
	resolved_addresses TEXT NOT NULL DEFAULT '[]',
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX domains_site_kind_idx ON domains (site_id, kind, hostname);
--bun:split
CREATE TABLE domain_plans (
	domain_id TEXT PRIMARY KEY REFERENCES domains(id) ON DELETE CASCADE,
	plan_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
