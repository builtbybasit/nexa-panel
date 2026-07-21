CREATE TABLE mysql_family_engines (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	version TEXT NOT NULL,
	version_text TEXT NOT NULL,
	port INTEGER NOT NULL UNIQUE,
	status TEXT NOT NULL,
	socket_path TEXT NOT NULL,
	systemd_unit TEXT NOT NULL UNIQUE,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE TABLE mysql_accounts (
	id TEXT PRIMARY KEY,
	engine_id TEXT NOT NULL REFERENCES mysql_family_engines(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	host TEXT NOT NULL DEFAULT 'localhost',
	status TEXT NOT NULL,
	credential_ciphertext TEXT,
	pending_credential_ciphertext TEXT,
	pending_secret_digest TEXT,
	credential_revealed BOOLEAN NOT NULL DEFAULT FALSE,
	credential_version INTEGER NOT NULL DEFAULT 0,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	UNIQUE(engine_id, name, host)
);
--bun:split
CREATE TABLE mysql_databases (
	id TEXT PRIMARY KEY,
	engine_id TEXT NOT NULL REFERENCES mysql_family_engines(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	owner_account_id TEXT NOT NULL REFERENCES mysql_accounts(id),
	status TEXT NOT NULL,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	UNIQUE(engine_id, name)
);
--bun:split
CREATE TABLE mysql_grants (
	id TEXT PRIMARY KEY,
	database_id TEXT NOT NULL REFERENCES mysql_databases(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL REFERENCES mysql_accounts(id) ON DELETE CASCADE,
	access TEXT NOT NULL,
	status TEXT NOT NULL,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	UNIQUE(database_id, account_id)
);
--bun:split
CREATE TABLE mysql_restore_points (
	id TEXT PRIMARY KEY,
	database_id TEXT NOT NULL REFERENCES mysql_databases(id) ON DELETE CASCADE,
	status TEXT NOT NULL,
	path TEXT NOT NULL UNIQUE,
	sha256 TEXT,
	size_bytes INTEGER,
	verified_at TIMESTAMP,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE TABLE mysql_family_plans (
	id TEXT PRIMARY KEY,
	resource_type TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	operation TEXT NOT NULL,
	plan_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX mysql_family_plans_resource_idx ON mysql_family_plans(resource_type, resource_id, created_at DESC);
