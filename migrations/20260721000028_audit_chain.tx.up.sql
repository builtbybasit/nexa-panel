ALTER TABLE audit_events ADD COLUMN prev_hash TEXT NOT NULL DEFAULT '';
--bun:split
ALTER TABLE audit_events ADD COLUMN hash TEXT NOT NULL DEFAULT '';
