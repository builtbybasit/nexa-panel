CREATE TABLE backup_accounts (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	type TEXT NOT NULL,
	path TEXT NOT NULL,
	config_json TEXT NOT NULL,
	secret_ciphertext TEXT,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
