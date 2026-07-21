CREATE TABLE postgresql_instances (
	id TEXT PRIMARY KEY,
	version TEXT NOT NULL,
	cluster_name TEXT NOT NULL,
	port INTEGER NOT NULL UNIQUE,
	status TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT 'postgres',
	data_path TEXT NOT NULL UNIQUE,
	socket_path TEXT NOT NULL,
	log_path TEXT NOT NULL UNIQUE,
	config_path TEXT NOT NULL UNIQUE,
	systemd_unit TEXT NOT NULL UNIQUE,
	managed_by_nexa BOOLEAN NOT NULL DEFAULT FALSE,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	UNIQUE(version, cluster_name)
);
--bun:split
CREATE TABLE database_roles (
	id TEXT PRIMARY KEY,
	instance_id TEXT NOT NULL REFERENCES postgresql_instances(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
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
	UNIQUE(instance_id, name)
);
--bun:split
CREATE TABLE managed_databases (
	id TEXT PRIMARY KEY,
	instance_id TEXT NOT NULL REFERENCES postgresql_instances(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	owner_role_id TEXT NOT NULL REFERENCES database_roles(id),
	status TEXT NOT NULL,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	UNIQUE(instance_id, name)
);
--bun:split
CREATE TABLE database_grants (
	id TEXT PRIMARY KEY,
	database_id TEXT NOT NULL REFERENCES managed_databases(id) ON DELETE CASCADE,
	role_id TEXT NOT NULL REFERENCES database_roles(id) ON DELETE CASCADE,
	access TEXT NOT NULL,
	status TEXT NOT NULL,
	last_job_id INTEGER REFERENCES jobs(id),
	failure TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	UNIQUE(database_id, role_id)
);
--bun:split
CREATE TABLE database_restore_points (
	id TEXT PRIMARY KEY,
	database_id TEXT NOT NULL REFERENCES managed_databases(id) ON DELETE CASCADE,
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
CREATE TABLE postgresql_plans (
	id TEXT PRIMARY KEY,
	resource_type TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	operation TEXT NOT NULL,
	plan_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX postgresql_plans_resource_idx ON postgresql_plans(resource_type, resource_id, created_at DESC);
