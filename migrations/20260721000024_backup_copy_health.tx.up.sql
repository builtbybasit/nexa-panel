ALTER TABLE backup_copies ADD COLUMN integrity_state TEXT NOT NULL DEFAULT 'unverified';
--bun:split
ALTER TABLE backup_copies ADD COLUMN integrity_checked_at TIMESTAMP;
--bun:split
ALTER TABLE backup_copies ADD COLUMN restore_test_state TEXT NOT NULL DEFAULT 'not_tested';
--bun:split
ALTER TABLE backup_copies ADD COLUMN restore_tested_at TIMESTAMP;
--bun:split
ALTER TABLE backup_copies ADD COLUMN health_error TEXT;
