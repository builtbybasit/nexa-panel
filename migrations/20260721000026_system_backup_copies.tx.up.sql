CREATE TABLE system_backup_copies (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES backup_accounts(id),
	copy_name TEXT NOT NULL,
	remote_path TEXT NOT NULL,
	size_bytes INTEGER NOT NULL,
	sha256 TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX idx_system_backup_copies_account ON system_backup_copies(account_id);
