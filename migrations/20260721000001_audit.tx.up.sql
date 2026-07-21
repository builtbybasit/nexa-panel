CREATE TABLE audit_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	occurred_at TIMESTAMP NOT NULL,
	actor_user_id TEXT,
	action TEXT NOT NULL,
	subject TEXT NOT NULL,
	remote_address TEXT NOT NULL DEFAULT '',
	metadata TEXT NOT NULL DEFAULT '{}'
);
--bun:split
CREATE INDEX audit_events_occurred_at_idx ON audit_events (occurred_at DESC);
