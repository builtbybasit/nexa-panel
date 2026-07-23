CREATE TABLE lockout_reverts (
	id TEXT PRIMARY KEY,
	domain TEXT NOT NULL,
	subject TEXT NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	reasons_json TEXT NOT NULL DEFAULT '[]',
	payload_json TEXT NOT NULL DEFAULT '{}',
	actor_user_id TEXT,
	job_id INTEGER NOT NULL DEFAULT 0,
	revert_job_id INTEGER NOT NULL DEFAULT 0,
	armed_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	confirmed_at TIMESTAMP,
	state TEXT NOT NULL,
	failure TEXT NOT NULL DEFAULT ''
);
--bun:split
CREATE INDEX lockout_reverts_state_idx ON lockout_reverts (state, expires_at);
--bun:split
CREATE INDEX lockout_reverts_domain_idx ON lockout_reverts (domain, armed_at);
