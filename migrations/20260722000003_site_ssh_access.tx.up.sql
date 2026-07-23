CREATE TABLE site_ssh_access (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	enabled BOOLEAN NOT NULL DEFAULT 0,
	username TEXT NOT NULL,
	shell TEXT NOT NULL DEFAULT '/usr/sbin/nologin',
	enabled_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE TABLE site_ssh_keys (
	id TEXT PRIMARY KEY,
	site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	label TEXT NOT NULL,
	algorithm TEXT NOT NULL,
	public_key TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	comment TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	UNIQUE(site_id, fingerprint)
);
--bun:split
CREATE INDEX site_ssh_keys_site_idx ON site_ssh_keys (site_id, created_at);
