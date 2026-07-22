# Runbook: Restore panel state (control.db + master.key) onto a fresh node

This restores the panel's **own** state — the control database (`control.db`,
holding every user, grant, and encrypted secret) and the AES master key
(`master.key`, which decrypts those secrets) — from a panel-state ("system")
backup. Without both files a fresh node cannot decrypt any stored credential, so
this is the disaster-recovery path for a lost disk.

Site and database backups are restored through the panel UI; this runbook covers
only the panel's own state.

## Security — read first

The panel-state archive (`nexa-panel-system.tar.gz`) contains `master.key` plus
the AES-encrypted secrets from `control.db`. Together they are
**credential-equivalent**: anyone holding the archive can recover every stored
database and site credential in plaintext.

- Store panel-state backups **only on a trusted, encrypted remote** — e.g. an
  `rclone crypt` remote, or a bucket with server-side encryption and tight IAM.
- The archive and the restored files are written `0600`; keep them that way.
- The panel never logs key or secret bytes — only the copy name, size, and
  SHA-256. Do not paste archive contents into logs or tickets.

## What you need

- The fresh node with `nexa` installed (`scripts/install.sh` has run, services
  exist but the panel has no state yet).
- `rclone` on the node (installed by the panel node setup).
- The rclone remote definition for the storage account the backup was uploaded
  to (endpoint + credentials). Because `control.db` is exactly what is being
  restored, the node cannot read the stored account — you must supply the remote
  config yourself (env vars, a temporary `rclone config`, or flags).
- The copy name to restore (the timestamped directory under `<path>/system/`).

## Steps

1. **Stop the panel services** so nothing holds the state files open:

   ```
   systemctl stop nexa-api nexa-agent
   ```

2. **Fetch the archive** from the remote into a working directory. Substitute
   your remote name, base path, and the copy name you want:

   ```
   rclone copy <remote>:<path>/system/<copyName>/ /root/panel-restore/
   ```

   You should now have `/root/panel-restore/nexa-panel-system.tar.gz`.

3. **Extract it into place.** `system-restore` extracts *exactly* `control.db`
   and `master.key` (it refuses any archive that carries other or traversing
   members) and writes both `0600`. It refuses to overwrite an existing
   non-empty `control.db` unless you pass `--force`:

   ```
   nexa backup system-restore \
     --archive /root/panel-restore/nexa-panel-system.tar.gz \
     --state /var/lib/nexa-panel/control.db \
     --master-key /var/lib/nexa-panel/master.key
   ```

4. **Fix ownership and permissions.** The state files are owned by the `nexa`
   user; the extractor writes them as the invoking user (usually `root`):

   ```
   chown nexa:nexa /var/lib/nexa-panel/control.db /var/lib/nexa-panel/master.key
   chmod 600 /var/lib/nexa-panel/control.db /var/lib/nexa-panel/master.key
   ```

5. **Start the services** and verify:

   ```
   systemctl start nexa-agent nexa-api
   ```

   Sign in with a known admin account. Confirm that stored backup accounts,
   database users, and site secrets resolve (e.g. a storage-account "Test
   connection" succeeds) — that proves `master.key` matches the restored
   `control.db`.

6. **Securely delete the working copy** once the panel is confirmed healthy:

   ```
   shred -u /root/panel-restore/nexa-panel-system.tar.gz
   ```

## Notes

- `control.db` in the archive is a consistent snapshot taken with SQLite
  `VACUUM INTO`, not a raw copy of a live WAL database, so it opens cleanly with
  no WAL replay needed.
- A `master.key` that does not match the `control.db` it was backed up with
  cannot decrypt the stored secrets. Always restore the two files **from the
  same copy**.
- Creating panel-state backups: `nexa backup system --account <id|name>` (or the
  `POST /api/v1/backups/system` API). Scheduling these on a timer is a planned
  follow-up; for now run it after significant changes and/or from cron.
