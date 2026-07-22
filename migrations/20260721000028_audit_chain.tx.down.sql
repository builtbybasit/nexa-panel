ALTER TABLE audit_events DROP COLUMN hash;
--bun:split
ALTER TABLE audit_events DROP COLUMN prev_hash;
