ALTER TABLE mysql_databases ADD COLUMN size_bytes INTEGER;
--bun:split
ALTER TABLE mysql_databases ADD COLUMN size_observed_at TIMESTAMP;
