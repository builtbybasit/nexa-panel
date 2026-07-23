# Lifecycle CI coverage

This is the honest status of the OPS-001 release gate: which of the eleven
required lifecycle scenarios execute in CI today, on which architectures, and
exactly what infrastructure the rest still need. It is deliberately written so
that "not covered" is as visible as "covered" — a scenario that is not in the
first table is not being tested, however many unit tests surround it.

## What runs in CI

`.github/workflows/ci.yml` runs four kinds of job:

| Job | What it is |
| --- | --- |
| `quality` | `make ci` — build, unit tests, contracts, ShellCheck, production build. |
| `lifecycle` | Executed host scenarios against a privileged systemd container. |
| `database-acceptance` | The destructive real-engine restore suites. |
| `vm-lifecycle` | The uncovered scenarios, rendered as named **Skipped** jobs. |

Both runtime jobs run on `ubuntu-24.04` and `ubuntu-24.04-arm`, so every
scenario below is executed on AMD64 and ARM64.

| Scenario | AMD64 | ARM64 | Where it executes |
| --- | --- | --- | --- |
| Idempotent re-run | yes | yes | `scripts/test-node-lifecycle.sh` scenario 1 |
| Failed preflight | yes | yes | `scripts/test-node-lifecycle.sh` scenario 2 |
| Uninstall retain-data | yes | yes | `scripts/test-node-lifecycle.sh` scenario 3 |
| Reinstall after retain | yes | yes | `scripts/test-node-lifecycle.sh` scenario 4 |
| Purge uninstall | yes | yes | `scripts/test-node-lifecycle.sh` scenario 5 |
| MySQL/MariaDB restore | yes | yes | `scripts/test-db-acceptance.sh mysql` |
| PostgreSQL restore | yes | yes | `scripts/test-db-acceptance.sh postgres` |

These assert on the real host — filesystem, ownership, systemd unit state,
accounts, and the API — never on the source text of `install.sh`. The
source-text assertions in `scripts/lifecycle_contract_test.go` remain useful as
a fast structural gate, but they are not what closes this section of the
release gate.

The database suites are skipped by a plain `go test ./...` on purpose: each
destroys and restores a real database. `NEXA_MYSQL_INTEGRATION=1` and
`NEXA_POSTGRES_INTEGRATION=1` are set in exactly two places —
`scripts/test-db-acceptance.sh` and the `database-acceptance` CI job that calls
it — so the gate has one documented way to be opened.

Run them locally the same way CI does:

```sh
make test-db-acceptance SUITE=mysql
make test-db-acceptance SUITE=postgres
docker build -t nexa-node .
make test-node-lifecycle BINARY=dist/nexa-linux-arm64
```

Rebuild the `nexa-node` image before running the host lifecycle scenarios. The
scenarios re-run the installer from the working tree against a node the *image*
installed, so a stale image reports drift that a real host would never show —
`packaging/` changes only reach the node through a rebuild.

## What does NOT run in CI

The remaining scenarios cannot be executed on a GitHub-hosted runner, and are
not simulated. `scripts/test-vm-lifecycle.sh` implements them for an operator or
a VM runner to invoke; nothing in `.github/workflows/` calls it.

They are still visible in every CI run. The `vm-lifecycle` job in
`.github/workflows/ci.yml` expands one matrix entry per scenario per
architecture, each named with the reason it did not run, and GitHub renders them
as **Skipped** in the checks list. An uncovered scenario is therefore as visible
as a covered one; turning them on means pointing `runner:` at a VM runner, not
deleting the entries.

| Scenario | Entry point | Why the container node cannot prove it |
| --- | --- | --- |
| Fresh TLS install (+ reboot) | `test-vm-lifecycle.sh fresh-tls`, then `reboot --arm` / `reboot --verify` | Let's Encrypt must reach the machine on a public DNS name over :80/:443 to issue a real certificate, and restarting a container's PID 1 is not a host boot. |
| Uninstall then flagless reinstall | `test-vm-lifecycle.sh reinstall` | The recovery is only real if a retained Let's Encrypt certificate is re-deployed and a public HTTPS request comes back on the original hostname; a container has neither. |
| N-1 → N update | `test-vm-lifecycle.sh update` | The node image bind-mounts `/usr/bin/nexa` read-only, so the atomic binary swap cannot happen; it also needs two distinct built releases. |
| Injected update failure | `test-vm-lifecycle.sh update-failure` | Requires a live activation to fail and the operator to restore the binary *and* the systemd unit graph on a host it really owns. |
| Offline rollback | `test-vm-lifecycle.sh offline-rollback` | Only meaningful if `nexa-api` and `nexa-agent` are genuinely stopped on a host that must still recover. |

## Running the VM matrix by hand

This is the procedure for one operator with one throwaway Ubuntu 24.04 server.
It is the only way these six scenarios are currently proven, so run it before
any release tag.

**Preconditions.** All five are hard requirements; the script refuses rather
than degrades if one is missing.

1. A **fresh** Ubuntu 24.04 server with root access and nothing else on it. The
   run installs, updates, rolls back, and reboots the machine, and writes a test
   site under `/srv/nexa`. Destroy it afterwards.
2. A **public DNS A/AAAA record** pointing at the server, with inbound :80 and
   :443 reachable from the Internet. `fresh-tls` obtains a real Let's Encrypt
   certificate and then fetches the panel with `curl` and no `-k`, so a staging
   or self-signed certificate fails the scenario by design.
3. An **unpacked release tree at `/opt/nexa-src`** — the `scripts/` and
   `packaging/` directories from a release tarball. Override the location with
   `NEXA_VM_SOURCE_DIR`.
4. **Two built binaries**, N-1 and N, with different versions.
   `scripts/build-linux-release.sh <arch>` produces them; build or download the
   previous release tag for N-1.
5. This script, copied to the server.

**The run.** Two commands, because a script cannot outlive the reboot it
requests. The reboot is deliberately the last scenario so there is exactly one
reconnect.

```sh
sudo bash test-vm-lifecycle.sh all \
  --hostname panel.example.com --tls-email ops@example.com \
  --previous /root/nexa-n-1 --target /root/nexa-n
# type: destroy this host          (or pass --yes to skip the prompt)
# ... scenarios 1-5 run, then the machine reboots ...

# reconnect over SSH, then:
sudo bash test-vm-lifecycle.sh all --resume
```

**The result.** `all` prints a numbered line per scenario and exits nonzero if
any scenario failed or never ran:

```
===== VM lifecycle results =====
1. PASS     fresh TLS install
2. PASS     uninstall then flagless reinstall
3. PASS     N-1 -> N update
4. PASS     offline rollback with the services stopped
5. PASS     injected update failure
6. PASS     reboot

6 passed, 0 failed, 0 not run
```

A failure stops the chain — later scenarios depend on the state earlier ones
leave — and the summary names the scenario that stopped it, with everything
after it marked `NOT RUN`. The order is fixed and not arbitrary:
`offline-rollback` consumes the succeeded transaction that `update` wrote, and
`update-failure` leaves that transaction in its failed state.

Each scenario is still individually runnable (`fresh-tls`, `reinstall`, `update`,
`offline-rollback`, `update-failure`, `reboot --arm` / `reboot --verify`) for
re-testing one thing after a fix; `test-vm-lifecycle.sh --help` prints the
sequence.

### Infrastructure still required

Running it by hand closes the gate once. Keeping it closed needs a VM runner.
Concretely:

1. **Ephemeral Ubuntu 24.04 VMs, AMD64 and ARM64**, root access, destroyed after
   every run. Any cloud instance or a self-hosted runner that provisions one
   works; the scripts assume nothing about the provider.
2. **A DNS name per run pointing at the VM's public address**, with inbound
   :80 and :443 open. `fresh-tls` does not accept a staging or self-signed
   certificate — it asserts the chain is not self-signed and that `curl`
   validates it against the system trust store. A wildcard zone the runner can
   write an A record into is the usual answer. Let's Encrypt rate limits apply,
   so the runner should use the ACME staging directory for pull-request runs and
   production issuance on a schedule.
3. **Reboot and reconnect.** The reboot phase calls `systemctl reboot` and
   returns nothing; the orchestrator has to wait for SSH and then invoke
   `all --resume`. A script cannot outlive the reboot it requests, so this is two
   invocations by design.
4. **Two built releases (N-1 and N)** staged on the VM for the `update`
   scenario. `scripts/build-linux-release.sh` produces them; the previous
   release tag has to be built or downloaded as well.
5. **A release tree at `/opt/nexa-src`** (override with `NEXA_VM_SOURCE_DIR`)
   containing `scripts/install.sh` and `packaging/`, i.e. an unpacked release
   tarball.

An orchestrator drives the same two commands as the by-hand procedure above,
with `--yes` instead of the typed confirmation, and waits for SSH between them.

### A note on the injected failure

`update-failure` installs an artifact that answers `nexa version` and fails at
everything else. That is not a shortcut: the operator validates a candidate by
executing it, so a build which passes validation and then cannot serve is
precisely the release that must roll the host back completely. The subject under
test is the operator's rollback — binary, packaging, unit graph, and control
database — not the artifact.

## Known failure: the idempotent re-run scenario

Scenario 1 currently **fails on a real node**, and the failure is a product bug,
not a test bug. It is recorded here rather than suppressed, because a scenario
edited until it passes proves nothing:

```
-/var/lib/nexa-panel/control.db nexa:www-data 600 f
+/var/lib/nexa-panel/control.db nexa:nexa 600 f
lifecycle failure: the installer re-run changed the node
```

`nexa-api.service` runs as `User=nexa Group=www-data`, so every file the control
plane creates — including `control.db` and its `-wal`/`-shm` side files — lands
with group `www-data`. `packaging/tmpfiles/nexa-panel.conf` declares
`z /var/lib/nexa-panel/control.db* 0600 nexa nexa -`, and each install run calls
`systemd-tmpfiles --create`, which chowns it back. The two never agree, so every
installer run flips the group of the live state database.

The mode is `0600`, so no access is granted or withdrawn either way and this is
not a privilege bug — but it does mean the installer is not idempotent against
its own runtime, which is exactly what the release gate asks scenario 1 to prove.
Changing the tmpfiles group to `www-data` makes the whole suite pass; that file
is owned by the packaging work, not by these scripts.

## Architecture notes

Nothing in the executed set is architecture-gated: GitHub's Linux ARM64 runners
boot the same privileged systemd container and run the same `mysql:8.4`,
`mariadb:11.8`, and `postgres:18` images. Two ARM64-specific behaviours are
expected and are not failures:

- `install.sh` warns on ARM64 that upstream publishes no ARM64 packages for some
  MySQL series, so the Applications catalog offers fewer database versions.
- `scripts/test-db-acceptance.sh` cross-compiles the PostgreSQL suite for the
  *image's* architecture rather than the host's, because a Docker daemon will
  run an AMD64 image under emulation without saying so.
