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
  exist but the panel has no state yet). If you know in advance that you are
  rebuilding a node from a panel-state backup, run the installer with
  `--no-start`: it enables the units without starting them, so nothing mints a
  key before you restore the real one. If the services have already started,
  step 1 stops them and step 3 replaces the minted key — no reinstall needed.
- `rclone` on the node (installed by the panel node setup).
- The rclone remote definition for the storage account the backup was uploaded
  to (endpoint + credentials). Because `control.db` is exactly what is being
  restored, the node cannot read the stored account — you must supply the remote
  config yourself (env vars, a temporary `rclone config`, or flags).
- The copy name to restore (the timestamped directory under `<path>/system/`).

## Where the two files go — read before running anything

The two restored files land in **different directories**, and getting the key's
path wrong fails *silently*:

| File | Path | Owner | Mode |
| --- | --- | --- | --- |
| Control database | `/var/lib/nexa-panel/control.db` | `nexa:nexa` | `0600` |
| Master key | `/etc/nexa-panel/master.key` | `nexa:nexa` | `0600` |

`/var/lib/nexa-panel/master.key` is the **legacy** location, used by nodes
installed before the key was split away from the state directory. It is not
where the panel looks any more, and restoring the key there does not work on a
fresh node. The precedence the panel actually applies:

1. `nexa-api.service` runs a root `ExecStartPre` before every start. If
   `/etc/nexa-panel/master.key` is missing and `/var/lib/nexa-panel/master.key`
   exists, it *moves* the legacy key into place. If **both** are missing it
   **mints a brand-new random key** at `/etc/nexa-panel/master.key`. Then it
   chowns the key to `nexa:nexa` and chmods it `0600`.
2. The API resolves `/etc/nexa-panel/master.key` on its own (the unit passes no
   `--master-key`). If a key is already there, a key left at the legacy path is
   **ignored** — the panel only logs `a master key remains at the legacy
   location and is now unused` and carries on.

So on a fresh node the first start of `nexa-api` mints a key. If you then
restore the real key to `/var/lib`, the minted key wins, the API opens the
restored `control.db` with the wrong key, and every TOTP seed, managed database
credential and storage token fails to decrypt — with no startup error. **Always
restore the key to `/etc/nexa-panel/master.key`, with the services stopped.**
`system-restore` overwrites whatever is at that path, so a key minted by an
earlier start is replaced correctly as long as you use the right path.

## Steps

1. **Stop the panel services** so nothing holds the state files open and nothing
   re-mints a key underneath you. Do this even if you believe the services never
   started — `nexa-api` may have been started once by the installer:

   ```
   systemctl stop nexa-api nexa-agent
   ```

2. **Fetch the archive** from the remote into a working directory. Substitute
   your remote name, base path, and the copy name you want:

   ```
   rclone copy <remote>:<path>/system/<copyName>/ /root/panel-restore/
   ```

   You should now have `/root/panel-restore/nexa-panel-system.tar.gz`.

3. **Extract it into place**, as `root`, with the services still stopped.
   `system-restore` extracts *exactly* `control.db` and `master.key` (it refuses
   any archive that carries other or traversing members) and writes both `0600`,
   truncating whatever is already at each destination. It refuses to overwrite an
   existing non-empty `control.db` unless you pass `--force` — which you need
   here if the panel started once on this node and created its own database:

   ```
   install -d -m 0711 -o root -g root /etc/nexa-panel
   nexa backup system-restore \
     --archive /root/panel-restore/nexa-panel-system.tar.gz \
     --state /var/lib/nexa-panel/control.db \
     --master-key /etc/nexa-panel/master.key
   ```

   If that stops with `refusing to overwrite existing control database`, the
   panel already ran here and created its own empty state; re-run the exact same
   command with `--force` appended. There is nothing in that database worth
   keeping.

   Pass `--master-key` explicitly even though it is the default: this is the one
   argument that silently ruins the restore if it is wrong. Do **not** point it
   at `/var/lib/nexa-panel/master.key`.

4. **Remove any key left at the legacy path.** A rebuilt node should not have
   one, but if this node was upgraded in place, or an earlier attempt restored
   the key to the old location, it is now dead weight that only creates doubt
   about which key is live:

   ```
   shred -u /var/lib/nexa-panel/master.key 2>/dev/null || true
   ```

5. **Fix ownership and permissions.** Both files are owned by the `nexa` user;
   the extractor writes them as the invoking user (usually `root`). Note the two
   different directories:

   ```
   chown nexa:nexa /var/lib/nexa-panel/control.db /etc/nexa-panel/master.key
   chmod 600 /var/lib/nexa-panel/control.db /etc/nexa-panel/master.key
   ```

6. **Start the services** and verify:

   ```
   systemctl start nexa-agent nexa-api
   ```

   Sign in with a known admin account **from the backed-up panel** — if a
   bootstrap admin was printed when this node first started, that account came
   from the discarded database and no longer exists. Then confirm that stored
   backup accounts, database users, and site secrets resolve (e.g. a
   storage-account "Test connection" succeeds) — that proves `master.key`
   matches the restored `control.db`.

   If sign-in works but every stored secret fails to decrypt, you are running on
   the wrong key: stop the services and redo steps 3-5, checking the
   `--master-key` path.

7. **Securely delete the working copy** once the panel is confirmed healthy:

   ```
   shred -u /root/panel-restore/nexa-panel-system.tar.gz
   ```

## Notes

- `control.db` in the archive is a consistent snapshot taken with SQLite
  `VACUUM INTO`, not a raw copy of a live WAL database, so it opens cleanly with
  no WAL replay needed.
- A `master.key` that does not match the `control.db` it was backed up with
  cannot decrypt the stored secrets. Always restore the two files **from the
  same copy**. Nothing at startup detects a mismatch: the key file carries no
  identity, so a wrong key opens cleanly and only fails one secret at a time.
- The archive member is named `master.key` regardless of which path the node it
  came from used, so a backup taken from a pre-split node restores to the
  current `/etc/nexa-panel/master.key` path unchanged.
- Creating panel-state backups: `nexa backup system --account <id|name>` (or the
  `POST /api/v1/backups/system` API) runs one on demand.

## Scheduling automatic off-node backups

A live node should keep off-node copies without anyone remembering to run the
command. Nexa ships a systemd timer for this
(`nexa-panel-system-backup.timer` → `nexa-panel-system-backup.service`).
`scripts/install.sh` enables the timer by default, but it stays **dormant** — its
service is a no-op (`ConditionPathExists`) — until you tell it which backup
storage account (rclone remote) to ship to. So a fresh node produces no failure
spam, and the day you point it at a destination the daily backups just start.

(If you are upgrading a node that predates this timer, or ran the installer with
`--no-start`, enable it yourself as shown in step 3.)

1. **Choose the destination account.** In the panel, create (or pick) a backup
   storage account and note its ID or name. Follow the security guidance above:
   it must be a trusted, encrypted remote, since the archive is
   credential-equivalent.

2. **Point the timer at it.** Write the account into the service's environment
   file (root-owned, under `/etc/nexa-panel/`):

   ```
   printf 'NEXA_SYSTEM_BACKUP_ACCOUNT=%s\n' '<account id or name>' \
     > /etc/nexa-panel/system-backup.env
   chmod 0640 /etc/nexa-panel/system-backup.env
   ```

   Until this file exists the timer's service is a **no-op** (`ConditionPathExists`
   skips it cleanly), so a node that has not opted in never spams backup
   failures.

3. **Enable the timer:**

   ```
   systemctl enable --now nexa-panel-system-backup.timer
   ```

   It runs daily with a randomized delay and `Persistent=true` (a backup missed
   while the node was off runs once it returns). Trigger one immediately to
   verify wiring with `systemctl start nexa-panel-system-backup.service`, then
   confirm a new copy appears (panel Backups view or `nexa` logs / the
   `system_backup_copies` history).

> **Critical — store the remote's credentials off-node.** The timer only creates
> copies; it cannot help you *restore* if the rclone remote definition
> (endpoint + credentials for the destination account) lives only on the node
> that died. During a disaster recovery `control.db` is exactly what is being
> restored, so the node cannot read the stored account — you must supply the
> remote config yourself (see "What you need" above). Keep that remote
> definition somewhere independent of this node (a password manager, a second
> host, sealed offline storage).
