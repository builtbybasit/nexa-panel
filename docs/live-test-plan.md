# Nexa Panel — live server test plan

A literal, runnable procedure for qualifying a release candidate on a fresh,
throwaway Ubuntu 24.04 LTS server. Every stage states what to run, what must be
true afterwards, and what to capture if it is not.

**This machine will be destroyed.** The plan installs, breaks, rolls back,
reboots, uninstalls, and purges. Never run it against a host that serves
anything, and never point the DNS name at a host you care about.

Read `PLAN.md` section 3 for what each stage is qualifying. Section 0 below
lists what this plan cannot prove even when every stage passes.

---

## 0. What this plan proves, and what it cannot

Proves (never before executed on real hardware):

- A real Let's Encrypt certificate on a real DNS name, issued by the installer.
- A real host reboot and boot-clean recovery.
- A real `/usr/bin/nexa` swap, activation, health gate, and automatic rollback.
- Offline rollback with `nexa-api` and `nexa-agent` stopped.
- Installer rollback on a real host with real Nginx, UFW, and sshd.
- Retain-data uninstall, reinstall over retained state, and purge.

Does **not** prove, even if every stage passes:

- The GitHub release download, signature, and manifest-agreement chain. Stages
  G–I use `nexa self-update --binary`, which deliberately bypasses
  `verifyReleaseSignature` and `verifyReleaseAgreement`. Stage P covers it, and
  it requires two real published releases.
- AMD64 **and** ARM64 unless you run the whole plan twice, once on each.
- Browser journeys. There is no Playwright suite; stages M and N are manual.

---

## 1. Prerequisites

### 1.1 Server

| Item | Requirement |
| --- | --- |
| OS | Ubuntu 24.04 LTS, clean image, nothing else installed |
| Arch | Record it. Run the plan once per architecture you intend to ship. |
| RAM | 2 GiB minimum, 4 GiB comfortable (Podman + pgAdmin + two DB engines) |
| Disk | 20 GiB free |
| Access | Root SSH, or a sudo user. Keep **two** SSH sessions open at all times. |
| Console | Provider out-of-band/serial console access, tested before you begin. Stages J and K can lock SSH out. |

### 1.2 DNS

Create an **A record** (and AAAA if the host has IPv6) for the panel hostname,
pointing at the server, with a short TTL, **before** you start. Certbot's HTTP-01
challenge will fail otherwise and stage E will roll back.

```sh
# From your workstation, not the server:
dig +short A panel.example.com
dig +short AAAA panel.example.com     # must be empty or the server's v6 address
```

Both must resolve to the test server. An AAAA record pointing somewhere else is
the single most common cause of a mysterious certbot failure.

### 1.3 Inbound ports

Open at the provider's edge firewall/security group **before** starting:

| Port | Why |
| --- | --- |
| 22/tcp | SSH. Never close it. |
| 80/tcp | HTTP-01 challenge and the HTTP→HTTPS redirect |
| 443/tcp | The panel |

The installer never enables UFW and never guesses your SSH port. UFW stays
inactive unless you enable it in stage K.

### 1.4 Credentials and artifacts

- A **fine-grained GitHub token**, scoped to `builtbybasit/nexa-panel` only,
  read-only Contents. Needed only for stage P. See `docs/release-signing.md`.
- Two release bundles built from the branch under test, with **different
  versions** (N-1 and N). Build them on your workstation:

```sh
cd /path/to/nexa-panel
VERSION=1.0.0-rc1 bash scripts/build-linux-release.sh amd64
mv dist/nexa-linux-amd64 /tmp/nexa-n-1
mv dist/nexa-panel-linux-amd64.tar.gz /tmp/nexa-panel-n-1.tar.gz

VERSION=1.0.0-rc2 bash scripts/build-linux-release.sh amd64
mv dist/nexa-linux-amd64 /tmp/nexa-n
mv dist/nexa-panel-linux-amd64.tar.gz /tmp/nexa-panel-n.tar.gz
```

Substitute `arm64` for an ARM server. `build-linux-release.sh` runs `make check`
and needs network access; run it on a machine that has both.

Copy to the server:

```sh
scp /tmp/nexa-n-1 /tmp/nexa-n root@SERVER:/root/
scp /tmp/nexa-panel-n-1.tar.gz root@SERVER:/root/
```

### 1.5 Set up the source tree on the server

The installer runs out of an unpacked bundle. Both the installer and the VM
lifecycle script expect it at `/opt/nexa-src`.

```sh
ssh root@SERVER
mkdir -p /opt/nexa-src
tar -xzf /root/nexa-panel-n-1.tar.gz -C /tmp
mv /tmp/nexa-panel-*-linux-*/* /opt/nexa-src/
ls /opt/nexa-src            # expect: bin/ packaging/ scripts/
```

### 1.6 Shell variables used throughout

Set these in **both** SSH sessions:

```sh
export PANEL_HOST=panel.example.com
export TLS_EMAIL=ops@example.com
export CAPTURE=/root/live-test-capture
mkdir -p "$CAPTURE"
```

---

## 2. The procedure

Run the stages **in order**. Several are stateful: stage H consumes the
transaction stage G wrote, and stage I ends it in a failed state.

There is an automated driver for stages E, G, H, I, and J:

```sh
sudo bash /opt/nexa-src/scripts/test-vm-lifecycle.sh all \
  --hostname "$PANEL_HOST" --tls-email "$TLS_EMAIL" \
  --previous /root/nexa-n-1 --target /root/nexa-n
# type: destroy this host
# ... the machine reboots at the end; reconnect, then:
sudo bash /opt/nexa-src/scripts/test-vm-lifecycle.sh all --resume
```

**Run the stages manually the first time anyway.** The driver has never been
executed against a real VM — only its argument parsing has been exercised. Use
it as a second pass, or as the regression run on subsequent candidates.

---

### Stage A — Dry run (must mutate nothing)

```sh
cd /opt/nexa-src
bash scripts/install.sh --dry-run --panel-hostname "$PANEL_HOST" \
  --tls-email "$TLS_EMAIL" | tee "$CAPTURE/A-dry-run.txt"
echo "exit=$?"
```

Verify:

- Exit 0. Runs **without** `sudo` — root is not required for a dry run.
- The plan names your hostname, `INSTALL_PACKAGE` lines for the prerequisites
  and PHP, `WRITE` lines for every managed file, `ENABLE` for
  `nexa-api.service`, `nexa-agent.service`, `nexa-update-recovery.service`,
  `nexa-panel-system-backup.timer`, and a `VERIFY` line for public ingress.
- Nothing changed:

```sh
test ! -e /usr/bin/nexa && echo "no binary: ok"
test ! -e /etc/nexa-panel && echo "no config: ok"
id nexa 2>/dev/null && echo "FAIL: account created by a dry run"
systemctl list-unit-files 'nexa*' --no-legend | grep . && echo "FAIL: units installed"
```

**Capture on failure:** the full dry-run output and `bash -x` of the same command.

---

### Stage B — Preflight (must mutate nothing)

```sh
sudo /opt/nexa-src/bin/nexa doctor --preflight 2>&1 | tee "$CAPTURE/B-preflight.txt"
```

Verify:

- Exit 0 with no `BLOCKER` lines. Warnings about Apache being installed-but-
  stopped are expected on some images.
- If `Network: BLOCKER` appears, the host cannot reach `archive.ubuntu.com` or
  `ppa:ondrej/php`. **Fix the network; do not use `--skip-preflight`.** An
  offline install will fail halfway and exercise stage D by accident.
- Re-run the four "nothing changed" checks from stage A.

---

### Stage C — Injected installer failure and rollback

Do this **before** the real install, on a clean machine, so the assertion
"rollback leaves nothing behind" is meaningful.

Inject a fault that survives all read-only validation and fails only after
several managed files have been replaced:

```sh
cp /opt/nexa-src/packaging/nginx/nexa-panel-proxy.conf.template "$CAPTURE/proxy.orig"
echo 'this_is_not_a_directive;' >> /opt/nexa-src/packaging/nginx/nexa-panel-proxy.conf.template

cd /opt/nexa-src
sudo bash scripts/install.sh --panel-hostname "$PANEL_HOST" --tls-email "$TLS_EMAIL" \
  2>&1 | tee "$CAPTURE/C-injected-install.txt"
echo "exit=${PIPESTATUS[0]}"
```

Verify:

- **Exit is nonzero.**
- The output contains `RESTORE` lines and names the retained work directory.
- The host is clean again:

```sh
id nexa 2>/dev/null && echo "FAIL: service account survived rollback"
test -e /usr/bin/nexa && echo "FAIL: binary survived rollback"
ls /etc/nginx/sites-enabled/ | grep nexa && echo "FAIL: vhost survived"
systemctl list-unit-files 'nexa*' --no-legend | grep . && echo "FAIL: units survived"
sudo nginx -t          # must print "syntax is ok" / "test is successful"
sudo sshd -t           # must print nothing
grep -c '^Include' /etc/ssh/sshd_config     # must be 1
```

Restore the template before continuing:

```sh
cp "$CAPTURE/proxy.orig" /opt/nexa-src/packaging/nginx/nexa-panel-proxy.conf.template
```

**Capture on failure:** the retained rollback work directory (its path is printed
in the output — `tar -czf "$CAPTURE/C-workdir.tgz" <path>`), plus
`/var/log/nexa-panel-install.*.log`.

---

### Stage D — Fresh TLS install

This is the stage that has never run anywhere.

```sh
cd /opt/nexa-src
sudo bash scripts/install.sh \
  --panel-hostname "$PANEL_HOST" \
  --tls-email "$TLS_EMAIL" \
  --verbose 2>&1 | tee "$CAPTURE/D-install.txt"
echo "exit=${PIPESTATUS[0]}"
```

Expect 5–15 minutes: apt repositories, PHP, PostgreSQL, Podman, and certbot.

Verify — **all of these, in order**:

```sh
# 1. Exit code
echo "exit was: $?"                              # must be 0

# 2. Services
systemctl is-active nexa-api nexa-agent nginx     # three "active"
systemctl --failed --no-legend                    # must be empty
systemctl is-enabled nexa-update-recovery.service nexa-panel-system-backup.timer

# 3. Certificate is real, not self-signed
sudo certbot certificates | tee -a "$CAPTURE/D-certs.txt"
echo | openssl s_client -connect "$PANEL_HOST:443" -servername "$PANEL_HOST" 2>/dev/null \
  | openssl x509 -noout -issuer -dates
#    issuer must name Let's Encrypt / ISRG, NOT the host itself

# 4. Public ingress over TLS, from OUTSIDE the box
#    (run this from your workstation)
curl -sS -o /dev/null -w '%{http_code} %{ssl_verify_result}\n' \
  "https://$PANEL_HOST/api/v1/health/live"        # expect: 200 0

# 5. Plaintext redirects rather than serving
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "http://$PANEL_HOST/"
#    expect a 301/302 to https://

# 6. Privilege boundary (SEC-001)
ls -l /run/nexa-panel/                    # agent.sock root:nexa 660, api.sock nexa:www-data 660
ls -l /etc/nexa-panel/agent.token         # root:nexa 0640
id www-data                               # groups must be 33(www-data) ONLY — no nexa
sudo -u www-data cat /etc/nexa-panel/agent.token   # must be Permission denied
sudo -u www-data curl -sS --unix-socket /run/nexa-panel/agent.sock http://localhost/v1/health
#    must fail with a permission error, not return JSON

# 7. State ownership contract
ls -l /var/lib/nexa-panel/control.db*     # every file nexa:nexa 0600
ls -ld /var/lib/nexa-panel                # nexa:nexa 0700
ls -l /var/lib/nexa-panel/install/ownership.v1   # root:root 0600

# 8. Publishing record (LIF-002)
sudo nexa publishing show
#    Must report "Publishing: tls", the hostname you installed with, and
#    "Source:   install". "Source: inferred from ..." means the installer did not
#    record the publication and the reinstall in Stage O will downgrade the node.

# 9. First administrator
sudo cat /root/nexa-panel-first-admin.txt | tee "$CAPTURE/D-admin.txt"

# 10. Audit chain
sudo nexa audit verify                     # must report INTACT

# 11. Doctor on the finished node
sudo nexa doctor 2>&1 | tee "$CAPTURE/D-doctor.txt"   # no BLOCKER lines

# 12. Re-check ingress the way the installer does
sudo bash /opt/nexa-src/scripts/install.sh --verify-ingress
```

Then sign in through a browser at `https://$PANEL_HOST/` with the credentials
from step 9. Confirm the overview page loads and shows the node.

**Capture on failure:** `journalctl -u nexa-api -u nexa-agent -u nginx --no-pager -n 500`,
`/var/log/nexa-panel-install.*.log`, `/var/log/letsencrypt/letsencrypt.log`,
`nginx -T`, `systemctl --failed`, and `sudo nexa doctor`.

---

### Stage E — Idempotent re-run (no publishing or permission drift)

Take a fingerprint, re-run the installer with **no publishing flags**, and diff.

```sh
fingerprint() {
  sudo find /etc/nexa-panel /var/lib/nexa-panel /usr/lib/systemd/system \
       /etc/nginx/sites-available /etc/nginx/snippets /usr/bin/nexa \
       -name '*nexa*' -o -path '*nexa*' 2>/dev/null | sort | while read -r p; do
    sudo stat -c '%n %U:%G %a %s' "$p"
  done
}
fingerprint > "$CAPTURE/E-before.txt"
sudo cp /etc/nginx/sites-available/nexa-panel.conf "$CAPTURE/E-vhost-before.conf"

cd /opt/nexa-src
sudo bash scripts/install.sh --allow-existing 2>&1 | tee "$CAPTURE/E-rerun.txt"
echo "exit=${PIPESTATUS[0]}"

fingerprint > "$CAPTURE/E-after.txt"
diff -u "$CAPTURE/E-before.txt" "$CAPTURE/E-after.txt" && echo "NO DRIFT: ok"
sudo diff -u "$CAPTURE/E-vhost-before.conf" /etc/nginx/sites-available/nexa-panel.conf \
  && echo "VHOST UNCHANGED: ok"
```

Verify:

- Exit 0, no drift in the fingerprint (ignore only `control.db*` sizes).
- **The vhost still has its TLS listeners and certbot certificate paths.** This
  is the highest-risk assertion in the whole plan: on a certbot-managed vhost the
  installer decides what to do by grepping for the string `managed by Certbot`.
  If your certificate was issued in a way that does not leave that comment, the
  installer may re-render the vhost and drop TLS.
- Public ingress still answers on 443 from outside.
- Run it a **third** time and diff again. Twice is not idempotence.

**Capture on failure:** both fingerprints, both vhosts, `nginx -T`, the install
transcript.

---

### Stage F — Backup and restore drill

Do this before you start breaking the node, so the panel state you fall back on
is real.

```sh
# Create a site through the UI (https://$PANEL_HOST/sites/new), then:
echo 'live-test-canary' | sudo tee /srv/nexa/sites/<slug>/public/canary.html

sudo nexa backup system 2>&1 | tee "$CAPTURE/F-backup.txt"
ls -l /var/lib/nexa-panel/backups/
```

In the UI: create a PostgreSQL database and a MySQL database, insert a row in
each, take a backup copy of each, then use **Restore** and confirm it demands a
reviewed plan and a typed confirmation before it will overwrite. Confirm the
row comes back.

Verify: the restore dialog refuses an unknown archive type and does not proceed
without the typed confirmation.

**Capture on failure:** the job payload and result from the UI, plus
`journalctl -u nexa-api -n 300`.

---

### Stage G — Update N-1 → N

```sh
sudo nexa version | tee "$CAPTURE/G-version-before.txt"
sudo sha256sum /usr/bin/nexa | tee "$CAPTURE/G-digest-before.txt"

sudo nexa self-update --binary /root/nexa-n 2>&1 | tee "$CAPTURE/G-update.txt"
echo "exit=${PIPESTATUS[0]}"
```

Verify:

```sh
sudo nexa version                                    # must report the N version
systemctl is-active nexa-api nexa-agent nginx        # three "active"
systemctl --failed --no-legend                       # empty
curl -sS -o /dev/null -w '%{http_code}\n' "https://$PANEL_HOST/api/v1/health/ready"   # 200
sudo python3 -c "import json;print(json.load(open('/var/lib/nexa-panel/transaction.json'))['phase'])" \
  2>/dev/null || sudo cat /var/lib/nexa-panel-update/transaction.json | head -40
#    phase must be "succeeded"
sudo nexa audit verify                               # INTACT
cat /srv/nexa/sites/<slug>/public/canary.html        # customer data intact
```

Sign in again through the browser. The session should survive or re-prompt
cleanly, not hang.

**Capture on failure:** `/var/lib/nexa-panel-update/transaction.json`, the whole
`/var/lib/nexa-panel-update/` tree, and the three journals.

---

### Stage H — Injected update failure → automatic rollback

Build a fake release that passes the version probe and then refuses to serve:

```sh
cat > /root/nexa-broken <<'EOF'
#!/bin/sh
[ "$1" = "version" ] && { echo "99.0.0-injected-failure"; exit 0; }
echo "injected update failure" >&2
exit 1
EOF
chmod 0755 /root/nexa-broken

sudo sha256sum /usr/bin/nexa > "$CAPTURE/H-digest-before.txt"
sudo nexa self-update --binary /root/nexa-broken 2>&1 | tee "$CAPTURE/H-update.txt"
echo "exit=${PIPESTATUS[0]}"
```

Verify:

- **Exit is nonzero.** A zero exit here is a release-blocking failure.
- The host is back on N, completely:

```sh
sudo sha256sum /usr/bin/nexa                  # identical to H-digest-before.txt
sudo nexa version                             # the N version, not 99.0.0
systemctl is-active nexa-api nexa-agent nginx # three "active"
curl -sS -o /dev/null -w '%{http_code}\n' "https://$PANEL_HOST/api/v1/health/ready"
sudo cat /var/lib/nexa-panel-update/transaction.json | grep -o '"phase":"[a-z]*"'   # "failed"
sudo nexa audit verify                        # INTACT
cat /srv/nexa/sites/<slug>/public/canary.html
```

Sign in through the browser again.

**Capture on failure:** everything under `/var/lib/nexa-panel-update/`, the
journals, `systemctl --failed`, `ls -l /usr/bin/nexa*`.

---

### Stage I — Offline rollback with both services stopped

Stage H left the transaction `failed`, which cannot be rolled back. Re-run
stage G first so a `succeeded` transaction exists, then:

```sh
sudo systemctl stop nexa-api.service nexa-agent.service
systemctl is-active nexa-api nexa-agent        # two "inactive"

sudo nexa self-update rollback 2>&1 | tee "$CAPTURE/I-rollback.txt"
echo "exit=${PIPESTATUS[0]}"
```

Verify:

```sh
sudo nexa version                              # the N-1 version
systemctl is-active nexa-api nexa-agent nginx  # three "active" (rollback restarts them)
curl -sS -o /dev/null -w '%{http_code}\n' "https://$PANEL_HOST/api/v1/health/ready"
sudo nexa audit verify                         # INTACT
cat /srv/nexa/sites/<slug>/public/canary.html
```

Sign in through the browser. The **control database was restored from a
snapshot** — confirm the site, the databases, and the backup copies from stage F
are all still listed, and that nothing created between the update and the
rollback silently vanished without explanation.

**Capture on failure:** `/var/lib/nexa-panel-update/`, `ls -l /var/lib/nexa-panel/`,
the journals, and `sudo nexa doctor`.

---

### Stage J — Reboot

```sh
sudo reboot
```

Reconnect and verify:

```sh
systemctl is-active nexa-api nexa-agent nginx
systemctl --failed --no-legend                 # empty
systemctl status nexa-update-recovery.service --no-pager | head -20
#    expect inactive/dead with no failure: there is no interrupted transaction
ls -l /run/nexa-panel/                         # sockets recreated with the right owners
ls -l /var/lib/nexa-panel/control.db*          # still nexa:nexa 0600
curl -sS -o /dev/null -w '%{http_code}\n' "https://$PANEL_HOST/api/v1/health/ready"
sudo nexa audit verify
```

Sign in through the browser.

**Capture on failure:** `journalctl -b --no-pager | tail -500`,
`systemctl --failed`, `systemd-analyze blame | head -20`.

---

### Stage K — Lockout safety (do this with the console open)

**Open the provider's out-of-band console now.** This stage deliberately tries
to remove the rule that admits your SSH session.

```sh
sudo ufw --force enable
sudo ufw status numbered | tee "$CAPTURE/K-ufw-before.txt"
```

In the UI, go to **Firewall**:

1. Delete the rule allowing 22/tcp. The panel must refuse with a **409** and a
   reason naming SSH. Your session must survive.
2. Confirm the refusal in the typed dialog. The rule is removed **and** a
   2-minute automatic revert is armed, with a live countdown.
3. **Do not confirm the revert.** Wait. After ~2 minutes the rule must come back
   on its own. Verify from the server: `sudo ufw status | grep 22`.
4. Repeat, and this time press **Confirm** to disarm. Wait 3 minutes and verify
   the rule stays removed. Re-add it manually: `sudo ufw allow 22/tcp`.
5. Repeat step 2, and while the revert is armed run
   `sudo systemctl restart nexa-api`. The revert must still fire.

In the UI, go to **Services**:

6. Stop `nginx`. Refused with 409. Acknowledge; nginx stops and a revert is
   armed; wait and confirm nginx comes back on its own.
7. Stop `nexa-api`. Must be refused **unconditionally**, with a message pointing
   at a root console. There must be no way to acknowledge past it.

**Capture on failure:** `sudo ufw status numbered`, the 409 response bodies from
the browser network tab, `journalctl -u nexa-api -n 300`, and the
`lockout_reverts` table:
`sudo sqlite3 /var/lib/nexa-panel/control.db 'select * from lockout_reverts;'`

Then disable UFW again so later stages are not affected: `sudo ufw disable`.

---

### Stage L — Break-glass recovery

```sh
# Enrol MFA for the admin through the UI first, then:
sudo nexa mfa reset --user admin 2>&1 | tee "$CAPTURE/L-breakglass.txt"
```

Verify:

- Exit 0, and it works **while `nexa-api` is running**.
- Every session for that account is revoked — the browser must be signed out.
- Signing in again needs only the password; the enrolment prompt reappears.
- `sudo nexa mfa reset --user does-not-exist` exits nonzero with a clear message.
- `sudo nexa audit verify` still reports **INTACT** and the chain now contains
  `identity.mfa_break_glass_reset`.
- Stop `nexa-api` and run the reset again — it must still work with the API down.

---

### Stage M — Admin tools through the proxy

In the UI, Applications → install **phpMyAdmin** and **pgAdmin**. Then from a
database row, launch each.

Verify, with the browser network tab open:

- The dashboard loads with no HTTP 401 in the network log seconds after launch.
- Navigating inside the tool stays under `/tools/pgadmin/` and
  `/tools/phpmyadmin/`; no request escapes the prefix.
- No database password appears in any URL, in `localStorage`, or in
  `journalctl -u nexa-api`.
- Sign in as a second panel user; that user must not be able to reuse the first
  user's tool session URL.
- Leave it idle past the timeout, then relaunch. It must recover.

**Capture on failure:** a HAR export of the browser network log,
`journalctl -u nexa-api -n 500`, `podman ps -a`, and the pgAdmin container log.

---

### Stage N — Session isolation and expiry

- Sign in as admin in one browser profile. Sign in as a second, lower-privileged
  account in another profile. Confirm neither sees the other's data.
- In one profile: sign out, then sign in as the *other* account **in the same
  profile**. No page may show data from the first account, even briefly.
- Delete the session cookie from devtools and click around. The app must return
  to the auth gate **once**, stop polling, and not leave protected UI on screen.
- Open the file editor, type into it, then navigate away, sign out, and reload.
  Each must warn before discarding.

---

### Stage O — Uninstall, reinstall, purge

```sh
# 1. Plan first
sudo nexa uninstall --dry-run 2>&1 | tee "$CAPTURE/O-plan.txt"
#    Read it. Every RETAIN line must be data you expect to keep.

# 2. Retain-data uninstall
sudo nexa uninstall 2>&1 | tee "$CAPTURE/O-uninstall.txt"
echo "exit=${PIPESTATUS[0]}"

# Verify the program is gone and the data is not
test ! -e /usr/bin/nexa && echo "binary removed: ok"
systemctl list-unit-files 'nexa*' --no-legend | grep . && echo "FAIL: units remain"
test -f /var/lib/nexa-panel/control.db && echo "state retained: ok"
test -f /etc/nexa-panel/master.key && echo "key retained: ok"
test -f /etc/nexa-panel/publishing.json && echo "publication retained: ok"
test ! -e /etc/nginx/sites-available/nexa-panel.conf && echo "panel vhost removed: ok"
#    Those two together are the point: the vhost that expressed the publication
#    is gone and the record of it is not.
cat /srv/nexa/sites/<slug>/public/canary.html    # customer data intact
sudo nginx -t                                     # still valid
id www-data                                       # nexa group membership removed

# 3. Idempotence
sudo /opt/nexa-src/scripts/uninstall.sh 2>&1 | tail -5    # must exit 0, change nothing

# 4. Reinstall over retained state, with NO publishing flags
#    Deliberately flagless: the retained record is the only thing that can put
#    this node back on its hostname over HTTPS. Passing --panel-hostname here
#    would hide the defect this stage exists to catch.
cd /opt/nexa-src
sudo bash scripts/install.sh 2>&1 | tee "$CAPTURE/O-reinstall.txt"

# Verify it came back on the SAME hostname over HTTPS, with no -k
curl -fsS -o /dev/null -w '%{http_code}\n' "https://$PANEL_HOST/api/v1/health/ready"
curl -s -o /dev/null -w '%{http_code}\n' "http://$PANEL_HOST/"   # must be 30x
sudo nexa publishing show                          # tls, same hostname, Source: install
#    A loopback listener here is defect 7 regressing: check
#    grep listen /etc/nginx/sites-available/nexa-panel.conf

# Verify the OLD credentials still work — the master key and DB were retained
#    sign in with the ORIGINAL password from D-admin.txt
sudo nexa audit verify

# 5. Purge
sudo nexa uninstall --purge-data --dry-run 2>&1 | tee "$CAPTURE/O-purge-plan.txt"
#    Read every destructive path it lists.
sudo nexa uninstall --purge-data --yes 2>&1 | tee "$CAPTURE/O-purge.txt"

# Verify nothing the panel owned remains
test ! -e /usr/bin/nexa && test ! -e /var/lib/nexa-panel && test ! -e /etc/nexa-panel \
  && test ! -e /srv/nexa && echo "purged: ok"
id nexa 2>/dev/null && echo "FAIL: service account remains"
systemctl list-unit-files 'nexa*' --no-legend | grep . && echo "FAIL: units remain"
systemctl --failed --no-legend                    # empty
sudo nginx -t                                     # still valid
sudo reboot                                        # boot-clean check
# after reconnecting:
systemctl --failed --no-legend                    # still empty
```

**Capture on failure:** the uninstall plan and transcript, `systemctl --failed`,
`nginx -T`, `find / -name '*nexa*' -not -path '/proc/*' 2>/dev/null`.

---

### Stage P — The real release path (requires published releases)

Stages G–I used local binaries and **skipped signature and manifest
verification entirely**. This stage is the only one that exercises the path a
customer actually uses. It requires:

1. `NEXA_RELEASE_SIGNING_KEY` configured as a repository secret, and its public
   half matching `packaging/release-signers`.
2. Two tagged releases published by `.github/workflows/release.yml`.

On a freshly purged (or freshly created) server:

```sh
printf '%s' 'github_pat_...' | sudo tee /root/gh.token >/dev/null
sudo chmod 0600 /root/gh.token

sudo bash /opt/nexa-src/scripts/install.sh --download \
  --release-version vN-1 \
  --github-token-file /root/gh.token \
  --panel-hostname "$PANEL_HOST" --tls-email "$TLS_EMAIL" \
  2>&1 | tee "$CAPTURE/P-download-install.txt"

sudo shred -u /root/gh.token
```

Verify the transcript shows a `ssh-keygen -Y verify` success, not just a
`sha256sum -c`. Then:

```sh
sudo nexa self-update --check      # must report N-1 installed, N available
sudo nexa self-update 2>&1 | tee "$CAPTURE/P-selfupdate.txt"
sudo nexa version                  # N
```

Also test the negative cases explicitly:

```sh
sudo rm /etc/nexa-panel/release.token && sudo nexa self-update --check
#    must say the token is MISSING, not "release not found"
# restore a token whose scope is wrong, and confirm the message says
# "insufficient scope", not "rejected credential"
```

**Capture on failure:** the full transcript, `ls -l /var/lib/nexa-panel-update/`,
and the exact error text (the classification is the thing under test).

---

## 3. If something fails: the capture bundle

Do not tear the machine down. Collect this first:

```sh
sudo tar -czf /root/nexa-failure-$(date +%s).tgz \
  /var/log/nexa-panel-install.*.log \
  /var/lib/nexa-panel-update/ \
  /etc/nexa-panel/publishing.json \
  /etc/nginx/sites-available/ /etc/nginx/snippets/ \
  "$CAPTURE" 2>/dev/null

sudo journalctl -u nexa-api -u nexa-agent -u nginx -u nexa-update-recovery \
  --no-pager -n 2000 > /root/nexa-failure-journal.txt
sudo systemctl --failed --no-pager >> /root/nexa-failure-journal.txt
sudo nginx -T >> /root/nexa-failure-journal.txt 2>&1
sudo nexa doctor >> /root/nexa-failure-journal.txt 2>&1
sudo ufw status numbered >> /root/nexa-failure-journal.txt 2>&1
sudo ls -laR /run/nexa-panel /var/lib/nexa-panel >> /root/nexa-failure-journal.txt 2>&1
sudo cat /var/lib/nexa-panel-update/transaction.json >> /root/nexa-failure-journal.txt 2>&1
```

Then scp both files off before touching anything else. In particular, **do not
re-run the installer over a failed state** — that destroys the evidence of how
it failed.

---

## 4. Results template

| Stage | Result | Notes |
| --- | --- | --- |
| A Dry run | | |
| B Preflight | | |
| C Installer rollback | | |
| D Fresh TLS install | | |
| E Idempotent re-run | | |
| F Backup/restore drill | | |
| G Update N-1 → N | | |
| H Injected update failure | | |
| I Offline rollback | | |
| J Reboot | | |
| K Lockout safety | | |
| L Break-glass | | |
| M Admin tools | | |
| N Session isolation | | |
| O Uninstall / reinstall / purge | | |
| P Real release path | | |

Record the architecture, the exact `nexa version` output, and the commit the
bundles were built from at the top of the results.
