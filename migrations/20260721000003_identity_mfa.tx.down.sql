ALTER TABLE identity_sessions DROP COLUMN mfa_verified_at;
--bun:split
ALTER TABLE identity_users DROP COLUMN recovery_code_hashes;
--bun:split
ALTER TABLE identity_users DROP COLUMN totp_last_step;
--bun:split
ALTER TABLE identity_users DROP COLUMN totp_confirmed_at;
--bun:split
ALTER TABLE identity_users DROP COLUMN totp_secret_encrypted;
--bun:split
ALTER TABLE identity_users DROP COLUMN role;
