CREATE TABLE identity_users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	last_login_at TIMESTAMP
);
--bun:split
CREATE TABLE identity_sessions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES identity_users(id) ON DELETE CASCADE,
	token_hash BLOB NOT NULL UNIQUE,
	created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	last_seen_at TIMESTAMP NOT NULL,
	remote_address TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT ''
);
--bun:split
CREATE INDEX identity_sessions_expires_at_idx ON identity_sessions (expires_at);
