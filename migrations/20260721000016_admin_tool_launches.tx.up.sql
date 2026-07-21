CREATE TABLE admin_tool_launches (
	id TEXT PRIMARY KEY,
	actor_user_id TEXT NOT NULL,
	panel_user TEXT NOT NULL,
	tool_kind TEXT NOT NULL REFERENCES admin_tools(kind) ON DELETE CASCADE,
	source_engine TEXT NOT NULL,
	database_id TEXT NOT NULL,
	account_id TEXT NOT NULL,
	launch_token_hash TEXT NOT NULL UNIQUE,
	session_token_hash TEXT NOT NULL UNIQUE,
	session_ciphertext TEXT NOT NULL,
	upstream_cookie_name TEXT,
	used_at TIMESTAMP,
	expires_at TIMESTAMP NOT NULL,
	session_expires_at TIMESTAMP NOT NULL,
	created_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX admin_tool_launches_expiry_idx ON admin_tool_launches(session_expires_at);
