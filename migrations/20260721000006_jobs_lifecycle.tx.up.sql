ALTER TABLE jobs ADD COLUMN recovery_policy TEXT NOT NULL DEFAULT 'fail';
--bun:split
ALTER TABLE jobs ADD COLUMN idempotency_key TEXT;
--bun:split
ALTER TABLE jobs ADD COLUMN scope_site_ids TEXT NOT NULL DEFAULT '[]';
--bun:split
ALTER TABLE jobs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;
--bun:split
ALTER TABLE jobs ADD COLUMN lease_owner TEXT;
--bun:split
ALTER TABLE jobs ADD COLUMN lease_token TEXT;
--bun:split
ALTER TABLE jobs ADD COLUMN lease_expires_at TIMESTAMP;
--bun:split
CREATE UNIQUE INDEX jobs_kind_idempotency_idx
	ON jobs (kind, idempotency_key) WHERE idempotency_key IS NOT NULL;
--bun:split
CREATE INDEX jobs_running_lease_idx ON jobs (state, lease_expires_at);
