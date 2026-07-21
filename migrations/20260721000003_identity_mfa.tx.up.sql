ALTER TABLE identity_users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';
--bun:split
ALTER TABLE identity_users ADD COLUMN totp_secret_encrypted TEXT;
--bun:split
ALTER TABLE identity_users ADD COLUMN totp_confirmed_at TIMESTAMP;
--bun:split
ALTER TABLE identity_users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
--bun:split
ALTER TABLE identity_users ADD COLUMN recovery_code_hashes TEXT NOT NULL DEFAULT '[]';
--bun:split
ALTER TABLE identity_sessions ADD COLUMN mfa_verified_at TIMESTAMP;
