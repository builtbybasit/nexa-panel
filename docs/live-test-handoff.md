# Live-test handoff

**For:** the next agent or engineer continuing live-node qualification.
**Written:** 2026-07-23, at the end of the first real-hardware session.
**Read first:** `PLAN.md` section 10, then `docs/live-test-plan.md`.

---

## 1. Where things stand

Seven release blockers were found on real hardware in one session. Six were
fixed during that session; the seventh — publishing state — was fixed afterwards
and is described below. All work is committed on `main` (merged from
`release/v1-hardening`). `main` is **ahead of `origin/main` and has not been
pushed** — that was left as the user's call.

Full local suite is green: `go build`, `go vet`, `gofmt`, `go test ./...`,
frontend `test`/`typecheck`/`build`, OpenAPI lint, staticcheck, deadcode.

### Commits from the live session, newest last

| Commit | What it fixes |
| --- | --- |
| `3ddd1b6` | API refused its own Unix socket (426) — no TLS install could complete |
| `09a12b9` | Installer: imply `--allow-existing` on a validated re-run; roll back on `SIGTERM` |
| `5251688` | Unrouted `/api/*` returned 200 + SPA HTML instead of JSON 404 |
| `345aa4b` | Reboot: lazily-created `/run` trees and the `Requires=` edge that killed the API |
| `b8dfa64` | Self-update installed any exit-zero binary and never recovered a stalled activation |

Fixed after that session:

| Commit | What it fixes |
| --- | --- |
| (this branch) | Defect 7 — publishing state is recorded in `/etc/nexa-panel/publishing.json` instead of inferred from the vhost |

Earlier in the same branch: the systemd `StateDirectory` ownership conflict that
handed `/var/lib/nexa-panel/install` to `www-data`.

### Defect 7, now fixed — read this before the reinstall test

**Publishing state used to be inferred from the vhost.** A retain-data uninstall
removes the panel vhost, so a reinstall could not recover the publishing mode and
silently dropped a public HTTPS node to loopback `127.0.0.1:8888`.

The installer now writes `/etc/nexa-panel/publishing.json` when it publishes, and
reads it back in preference to the vhost. `/etc/nexa-panel` survives a
retain-data uninstall, so the record does too; when the record outlives the
vhost, the vhost is re-rendered from it and `certbot install --nginx --cert-name
HOST --redirect` re-deploys the retained certificate without issuing anything. A
recorded HTTPS node whose certificate is also gone is now **refused with an
error** rather than downgraded. Full detail in `PLAN.md` section 10.3.

`nexa publishing show` should report `Source: install`, never `Source: inferred
from …`. If you see the latter on a node, its record has not been written yet —
one installer run, or `sudo nexa publishing migrate`, fixes it.

Proven in the container (`scripts/test-node-lifecycle.sh` scenario 4 now
reinstalls with **no publishing flags** and asserts the listener came back), and
**proven on hardware** on 2026-07-23: `test-vm-lifecycle.sh reinstall` passed on
`panel.panjnadvetclinic.com`, along with the whole six-scenario matrix. See
`PLAN.md` section 10.5.

---

## 2. The box used, and its final state

`root@172.236.155.203` — Linode, Ubuntu 24.04.4, **AMD64**, 2 GiB, 1 vCPU.
Hostname `panjnadvetclinic.com` via Cloudflare.

**Do not trust this box for further testing.** It has been installed,
rolled back, interrupted, bricked, repaired, rebooted, uninstalled and
reinstalled repeatedly. Its final state is a reinstall that downgraded to
loopback-only. The user is creating a fresh box; use that.

State at handoff: `nexa` 0.3.0, services active, site `testsite` present,
publishing loopback-only, HTTPS dead, certificate still in `/etc/letsencrypt`.

### Cloudflare caveat, this cost time

The DNS record was **proxied** (orange cloud), so it resolved to Cloudflare
anycast IPs and certbot's HTTP-01 challenge could never reach the origin. It must
be **grey-clouded (DNS only)** with the `A` record pointing at the server, and
**both `AAAA` records deleted** — Let's Encrypt prefers IPv6 and will fail
against a Cloudflare AAAA. Verify before installing:

```sh
dig +short A  <hostname>   # must be exactly the server IP
dig +short AAAA <hostname> # must be empty
```

Re-enabling the proxy after install is fine, but set SSL/TLS mode to
**Full (strict)** or Cloudflare talks to the origin in plaintext. Renewals need
port 80 reachable, so grey-cloud again or move to DNS-01.

---

## 3. How to run the next pass

Build and ship the bundle from a checkout of `main`:

```sh
bash scripts/build-linux-release.sh amd64          # VERSION=x.y.z to stamp a version
scp dist/nexa-panel-linux-amd64.tar.gz root@<box>:/root/
ssh root@<box> 'cd /root && mkdir -p bundle && tar -xzf nexa-panel-linux-amd64.tar.gz -C bundle'
```

Install (note the flag is `--panel-hostname`, **not** `--hostname`):

```sh
cd /root/bundle/nexa-panel-*/
bash scripts/install.sh --dry-run --binary bin/nexa            # must mutate nothing
bash scripts/install.sh --binary bin/nexa \
    --panel-hostname <hostname> --tls-email <email>
```

Run it under `nohup … > /root/install.log 2>&1 &` and poll, or it will outlive a
tool timeout. Two logs matter and they differ:

- `/root/install.log` — the **console** stream (fd 8). The seed helper and the
  real failure reason go here.
- `/var/log/nexa-panel-install.*.log` — the transcript. Does **not** contain the
  seed step's output. Defect 1 was invisible in the transcript for this reason.

The generated administrator password is written to
`/root/nexa-panel-first-admin.txt` when stdout is not a terminal. Parse it with
`awk '/Password:/{print $2}'`.

---

## 4. What is already proven — do not re-litigate, just re-confirm

Dry-run zero-mutation, preflight gating at 2 GiB, real certbot issuance, ingress
verification, admin seeding, HTTPS login with optional MFA, installer rollback
retaining the certificate, the timed lockout revert (nginx restored at ~117 s),
reboot recovery, self-update forward/rollback/offline-rollback, site provisioning
serving PHP 8.3.32, and retain-data uninstall preserving site data byte-for-byte.

Full detail with evidence: `PLAN.md` section 10.2.

A clean end-to-end pass on a fresh box is still worth doing, because six fixes
landed *during* the previous run and a seventh after it — no single install has
yet gone start to finish on unmodified code.

---

## 5. What to test next, in order

1. ~~**Uninstall → reinstall over HTTPS**~~ — **done** 2026-07-23, passed on
   hardware. `PLAN.md` 10.5.
2. ~~**Clean end-to-end install**~~ — **done** 2026-07-23; the whole six-scenario
   matrix passed on unmodified code.
2a. **Install one application from the panel** in any future matrix run. Defect 8
   made every apt install on every node impossible, and all six scenarios passed
   on a box where the Applications page could not install a thing. No scenario
   covers this yet.
3. **Injected update failure mid-activation** — kill the activation helper, pull
   power (hard reset via the provider console), and confirm the node returns to
   the old healthy version. Only the *validation* failure path was proven; the
   mid-activation crash path was not.
4. **Purge uninstall** — `nexa uninstall --purge-data --yes`. Never reached.
5. **Databases** — PostgreSQL and MySQL/MariaDB create, backup, destructive
   restore. Completely untested on hardware; the destructive suites are still
   opt-in behind `NEXA_MYSQL_INTEGRATION=1` / `NEXA_POSTGRES_INTEGRATION=1`.
6. **Admin tools** — phpMyAdmin and pgAdmin through `/tools/…`, including the
   idle-expiry behaviour that ADM-001 was opened for. Podman has never run here.
7. **Firewall with UFW active** — only the service-stop half of the lockout guard
   was exercised. Enabling UFW on a remote box is genuinely risky; have console
   access open first.
8. **ARM64** — the entire plan, once. Nothing has ever run on ARM64.
9. **The signed release chain** — `gh release list` is empty, so
   `--download`, signature verification and manifest agreement have never
   executed. This needs two real published releases.

---

## 6. Method notes that mattered

- **Probe with a bad input, not only a good one.** Defect 4 surfaced only because
  a deliberately nonsense API path was requested; defect 6 only because a 22-byte
  shell script was offered as an update.
- **Reboot.** Defect 5 presented as a healthy host: zero failed units, agent
  active, and the panel simply gone.
- **Read the console stream, not just the transcript**, and do not truncate the
  head of a log — an early `tail -35` hid the complete dry-run plan and led to a
  wrong claim that had to be retracted.
- **Watch for orphans.** `SIGTERM` to `install.sh` left the seed helper running
  with `ppid=1`; check `ps -eo pid,ppid,cmd | grep -E 'install\.sh|seed-admin'`.
- **`docker build` of the test node stalls on this workstation** at
  `Installing host prerequisites` — OrbStack's embedded DNS under buildkit. Kill
  and retry; it usually succeeds on the second attempt.
- Long SSH work should be backgrounded with an `until ! pgrep …` poll; several
  runs exceeded the foreground tool timeout.

---

## 7. Standing instruction from the user

MFA enrollment is **optional** and must stay optional — it was made mandatory
once and that was explicitly reverted because it made testing painful. Once a
factor *is* enrolled, enforcement must remain strict (unchallenged session stays
unauthenticated, step-up still required for sensitive actions). Do not
reintroduce mandatory enrollment.
