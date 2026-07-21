ALTER TABLE managed_databases ADD COLUMN size_bytes INTEGER;
--bun:split
ALTER TABLE managed_databases ADD COLUMN size_observed_at TIMESTAMP;
