# Install, dry run, and recovery from a failed install

`scripts/install.sh` (shipped in a release tarball, and installed on the node as
part of the bundle) is the only supported way to lay out a node. This runbook
covers what it changes, what a failure leaves behind, and how to recover.

## Review the plan first

```bash
sudo ./scripts/install.sh --dry-run --panel-hostname panel.example.com --tls-email ops@example.com
```

`--dry-run` prints the complete plan and changes nothing: no package, file,
unit, repository, firewall rule, service action — not even an install
transcript, and it takes no lifecycle lock. It does not require root, so the
plan can be reviewed before the maintenance window is booked.

The plan uses the same verbs as `nexa-uninstall --dry-run`:

| Line | Meaning |
| --- | --- |
| `PUBLISH …` | How the panel will be reachable when the run finishes |
| `INSTALL_PACKAGE <name>` | An Ubuntu package that will be installed |
| `RUN <command>` | A command that will be executed (apt, repository setup, `nginx -t`, `certbot`, `systemctl`, `ufw`) |
| `MKDIR <path>` | A directory that will be created |
| `WRITE <path> (mode …)` | A managed file that will be written atomically |
| `SYMLINK <path> -> <target>` | A symlink that will be created or repointed |
| `APPEND <path>` | A file that will be appended to (only `/etc/ssh/sshd_config`) |
| `REMOVE <path>` | Something that will be removed (only the stock Nginx `default` site) |
| `ENABLE <unit>` | A unit that will be enabled |
| `VERIFY <url>` | The public-ingress check that must answer HTTP 200 before success |
| `RETAIN <thing>` | Something the run explicitly will not change |

The plan is computed from the same declarations the install uses (the managed
file set, the prerequisite package list, the unit list, the firewall port rule),
so it cannot promise a different set of changes than the run makes.

## What the installer mutates

- **Ubuntu packages** — the prerequisite set, the selected PHP branch, Composer.
- **Package repositories** — `ppa:ondrej/php` and PGDG, plus the keyring files
  those tools write, only when they are not already configured.
- **Managed files** — the packaged systemd units, sysusers/tmpfiles
  declarations, the release signers, `/usr/lib/nexa-panel/uninstall.sh` (and its
  `/usr/sbin/nexa-uninstall` symlink), the API service drop-in, the ownership
  marker, the panel vhost and proxy snippet, and `/usr/bin/nexa`.
- **The service identity** — the `nexa` account/group and the managed roots
  `/etc/nexa-panel`, `/var/lib/nexa-panel`, `/var/log/nexa-panel`, `/srv/nexa`.
- **OpenSSH** — one `Include /etc/ssh/sshd_config.d/*.conf` line, appended only
  if the host's config lacks it, plus missing host keys (`ssh-keygen -A`).
- **Nginx** — the panel vhost, its `sites-enabled` symlink, and the stock
  `default` site (disabled, with its link target recorded for restoration).
- **Units** — enablement and start of the panel, Nginx, cron, ssh and the backup
  timer.
- **The firewall** — only with `--manage-firewall`, only when UFW is already
  active, and only the panel's own ports. UFW is never enabled, its defaults are
  never changed, and no SSH port is ever guessed.
- **TLS** — with `--tls-email`, Certbot obtains a certificate and rewrites the
  vhost.

## What a failed install leaves behind

Every one of those mutations is recorded in an append-only rollback journal
*before* it happens. Any non-zero exit replays that journal backwards:

- managed files are restored byte-for-byte (mode and owner included) or removed
  if the run created them;
- symlinks are repointed to their original target, including the Nginx
  `sites-enabled` link and the stock `default` site;
- the sshd_config `Include` line is reverted, and `sshd -t` is re-run;
- units this run enabled are disabled again, services this run started are
  stopped, and services that were already running are restarted against the
  restored files;
- UFW rules this run added are deleted, and rules it deleted are restored;
- repositories this run added are removed — unless packages were already
  installed from them, in which case the repository is kept and reported;
- on a fresh install, the `nexa` account and the managed roots this run created
  are removed, so the next attempt is not blocked by an orphaned identity;
- `systemctl daemon-reload` runs, and Nginx and OpenSSH are revalidated and
  reloaded.

The installer then exits non-zero. The rollback output is printed to the console
in the same `RESTORE` / `REMOVE` / `RETAIN` vocabulary as the plan.

Deliberately **not** undone:

- **Installed Ubuntu packages.** A dpkg transaction cannot be reversed safely
  from here; the rollback says so and leaves them. Remove them with `apt-get`
  if you want the host bit-clean.
- **TLS certificates** already issued under `/etc/letsencrypt`. They are
  harmless and re-used by the next attempt; a needless re-issue would burn the
  ACME rate limit.
- **The removal of `www-data` from the privileged `nexa` group.** That is a
  security repair (a web-server compromise must not reach the root agent), and a
  failed install is not a reason to hand the privilege back.
- **SSH host keys** generated by `ssh-keygen -A`. Deleting host keys would break
  existing SSH trust.

## If the rollback itself could not finish

If any undo step fails, the installer says so, keeps its working directory, and
prints the path. That directory holds `rollback.journal` and a `rollback/`
directory of the saved originals, so recovery can be finished by hand: each
journal record names the path, the saved copy, and the mode/owner to restore.

A rollback runs only for a process that is still alive. A hard kill or a power
loss mid-install leaves the host as it was at that moment; re-running the
installer is the supported recovery — every step is idempotent, and the
ownership marker keeps a re-run from adopting anything it does not own.

If a re-run refuses with *"pre-existing nexa identity or managed roots have no
ownership marker"*, inspect the node: either it is a genuine pre-v1 Nexa install
(re-run once with `--adopt-existing`) or something unrelated owns those paths and
they must be moved aside.

## Public ingress verification

Before the installer reports success — and before it prints administrator
credentials — it fetches `<published-url>/api/v1/health/live` through the
listener it just published, and fails the install if that does not answer HTTP
200. The unix-socket readiness probe alone proves only that the API process is
alive; it says nothing about whether Nginx is listening, whether the vhost being
served is the one that was written, or whether the firewall lets the port
through.

Re-run just that check at any time, without reinstalling:

```bash
sudo ./scripts/install.sh --verify-ingress
```

It derives the URL from the node's own vhost (hostname + TLS, or the configured
plaintext port), makes no changes, and exits non-zero if the panel does not
answer. Use it after a DNS, certificate or firewall change.

`NEXA_INGRESS_ATTEMPTS` and `NEXA_INGRESS_DELAY` control how long the check
waits (default: 30 attempts, 2 seconds apart).

## After any install

```bash
sudo nginx -t
systemctl --failed
sudo ./scripts/install.sh --verify-ingress
```

See also: [uninstall](uninstall.md), [self-update](self-update.md).
