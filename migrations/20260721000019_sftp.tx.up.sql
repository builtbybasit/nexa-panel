CREATE TABLE sftp_access (
	site_id TEXT PRIMARY KEY,
	enabled BOOLEAN NOT NULL DEFAULT 0,
	username TEXT NOT NULL,
	password_set_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL
);
