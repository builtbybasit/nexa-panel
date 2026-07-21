CREATE TABLE backup_plans (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES backup_accounts(id),
	copies_limit INTEGER NOT NULL,
	site_ids TEXT NOT NULL,
	database_ids TEXT NOT NULL,
	schedule TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE INDEX idx_backup_plans_account ON backup_plans(account_id);
