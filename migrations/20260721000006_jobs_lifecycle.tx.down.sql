DROP INDEX IF EXISTS jobs_running_lease_idx;
--bun:split
DROP INDEX IF EXISTS jobs_kind_idempotency_idx;
--bun:split
ALTER TABLE jobs DROP COLUMN lease_expires_at;
--bun:split
ALTER TABLE jobs DROP COLUMN lease_token;
--bun:split
ALTER TABLE jobs DROP COLUMN lease_owner;
--bun:split
ALTER TABLE jobs DROP COLUMN attempt;
--bun:split
ALTER TABLE jobs DROP COLUMN scope_site_ids;
--bun:split
ALTER TABLE jobs DROP COLUMN idempotency_key;
--bun:split
ALTER TABLE jobs DROP COLUMN recovery_policy;
