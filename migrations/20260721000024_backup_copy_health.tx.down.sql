ALTER TABLE backup_copies DROP COLUMN health_error;
--bun:split
ALTER TABLE backup_copies DROP COLUMN restore_tested_at;
--bun:split
ALTER TABLE backup_copies DROP COLUMN restore_test_state;
--bun:split
ALTER TABLE backup_copies DROP COLUMN integrity_checked_at;
--bun:split
ALTER TABLE backup_copies DROP COLUMN integrity_state;
