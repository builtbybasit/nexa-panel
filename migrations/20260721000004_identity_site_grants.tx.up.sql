CREATE TABLE identity_site_grants (
	user_id TEXT NOT NULL REFERENCES identity_users(id) ON DELETE CASCADE,
	site_id TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY (user_id, site_id)
);
