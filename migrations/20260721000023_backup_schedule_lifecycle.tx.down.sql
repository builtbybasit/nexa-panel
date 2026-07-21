DROP INDEX IF EXISTS backup_plans_schedule_state_idx;
--bun:split
ALTER TABLE backup_plans DROP COLUMN schedule_synced_at;
--bun:split
ALTER TABLE backup_plans DROP COLUMN schedule_error;
--bun:split
ALTER TABLE backup_plans DROP COLUMN schedule_state;
