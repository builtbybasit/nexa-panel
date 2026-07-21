CREATE TABLE backup_copies (
	id TEXT PRIMARY KEY,
	plan_id TEXT NOT NULL,
	account_id TEXT NOT NULL,
	copy_name TEXT NOT NULL,
	remote_path TEXT NOT NULL,
	size_bytes INTEGER NOT NULL,
	entries TEXT NOT NULL,
	status TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX idx_backup_copies_plan ON backup_copies(plan_id);
