CREATE TABLE site_deploy_keys (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	algorithm TEXT NOT NULL,
	public_key TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	key_version INTEGER NOT NULL DEFAULT 1,
	repository TEXT NOT NULL DEFAULT '',
	last_tested_at TIMESTAMP,
	last_test_ok BOOLEAN,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
