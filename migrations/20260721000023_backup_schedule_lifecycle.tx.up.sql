ALTER TABLE backup_plans ADD COLUMN schedule_state TEXT NOT NULL DEFAULT 'pending';
--bun:split
ALTER TABLE backup_plans ADD COLUMN schedule_error TEXT;
--bun:split
ALTER TABLE backup_plans ADD COLUMN schedule_synced_at TIMESTAMP;
--bun:split
CREATE INDEX backup_plans_schedule_state_idx ON backup_plans (schedule_state);
