# Nexa Panel

Nexa Panel is a self-hosted server-management platform for Ubuntu 24.04 LTS.
It combines a Go control plane, a separate privileged node agent, and a Vue 3
web application for managing sites, databases, certificates, deployments,
backups, scheduled work, and selected host services.

> **Pre-release:** the current tree is suitable for development and disposable
> test nodes, but it is not ready for an Internet-facing production server.
> Installation, update/rollback, private artifact distribution, uninstall, and
> several privilege/UI safety issues are explicit release gates in
> [PLAN.md](./PLAN.md).

## What is implemented

- Sites, domains, Let's Encrypt certificates, PHP runtimes, SFTP, files, logs,
  and deployment workflows
- PostgreSQL and MySQL/MariaDB databases, accounts, grants, backup, and restore
- Remote backup accounts, plans, retention, and panel-state disaster recovery
- Scheduled site tasks and durable background jobs
- Application/package catalog, native phpMyAdmin, and resource-limited pgAdmin
- Firewall and allowlisted service operations, system status, audit history, and
  preflight diagnostics
- Password sessions, role/site authorization, optional TOTP MFA, recovery codes,
  CSRF protection, and origin enforcement
- AMD64/ARM64 embedded Linux binaries and a work-in-progress self-update flow

## Architecture

The packaged system runs two instances of the same Go binary:

- `nexa api` runs as the unprivileged `nexa` account. It owns identity,
  authorization, durable jobs, audit records, SQLite state, the HTTP API, and
  the embedded frontend.
- `nexa agent` runs as root and performs narrowly modeled host operations. The
  API reaches it through an authenticated Unix socket.

Nginx is the public ingress and proxies to `/run/nexa-panel/api.sock`. The agent
listens on `/run/nexa-panel/agent.sock`. SQLite stores control-plane state in
`/var/lib/nexa-panel/control.db`; `/etc/nexa-panel/master.key` encrypts stored
credentials.

PostgreSQL, MySQL/MariaDB, Nginx, PHP-FPM, and phpMyAdmin run natively. Podman is
used only for pgAdmin, which has a memory ceiling and an automatic stop policy.
The Docker setup in this repository is a privileged systemd acceptance node,
not a production container deployment.

## Supported targets

- Ubuntu 24.04 LTS
- Linux AMD64 and ARM64 release artifacts
- systemd and Nginx
- Go 1.25.12 for development (pinned by `go.mod`)
- Bun 1.3.9 for frontend development (pinned by `web/package.json`)

Other distributions and production container deployment are not supported for
v1. The release matrix in [PLAN.md](./PLAN.md) must pass before the first stable
release.

## Development

Install the locked frontend dependencies and run the project quality gates:

```bash
make check
```

`make check` verifies Go formatting and module tidiness, vet, tests,
Staticcheck, production dead-code reachability, govulncheck, shell syntax and
ShellCheck when available, frontend tests/typechecking/build, Knip, dependency
advisories, and OpenAPI lint. CI additionally runs the race suite and compiles
the embedded production path:

```bash
make ci
```

Run the local processes in separate terminals:

```bash
make build
./bin/nexa agent
```

```bash
NEXA_MASTER_KEY=/tmp/nexa-panel/master.key ./bin/nexa api
```

```bash
make web-dev
```

Vite serves `http://127.0.0.1:5173` and proxies `/api` to the development API.
Development state, sockets, and the agent token default under
`/tmp/nexa-panel/`. Never reuse development tokens or keys on a server.

### Database acceptance tests

The destructive real-engine suites are opt-in and must run before a release:

```bash
NEXA_MYSQL_INTEGRATION=1 go test -run TestMySQLFamilyDestroyedDatabaseRestoreIntegration -v ./internal/platform/operators/mysql
```

The PostgreSQL suite documents its required PostgreSQL 18 test container in
`internal/platform/operators/postgres/integration_test.go`.

## Disposable Docker test node

`nexa-node` runs Ubuntu 24.04 and systemd in a privileged container. Build the
binary for the host architecture, then build and start the node:

```bash
bash scripts/build-linux-release.sh arm64
docker compose up --build -d
```

On AMD64, build `amd64` and set `NEXA_BIN=./dist/nexa-linux-amd64`. The panel is
published at `http://localhost:8888/`. This is deliberately plaintext and the
container prints its generated test administrator credentials to container
logs. Do not reuse this pattern, its credentials, or its filesystem-hardening
drop-in on a real server.

Useful smoke checks:

```bash
docker exec nexa-node systemctl is-active nexa-agent nexa-api nginx
docker exec nexa-node nexa doctor --preflight --allow-existing --json
docker exec nexa-node curl --unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/health/ready
```

The container bind-mounts `/usr/bin/nexa` read-only and discards panel state. It
therefore cannot prove self-update, rollback, retained-data reinstall, or
uninstall behavior.

## Release artifacts

Build the Linux artifacts for one architecture:

```bash
bash scripts/build-linux-release.sh amd64
bash scripts/build-linux-release.sh arm64
```

Each build creates a bare binary and an installable tar bundle plus SHA-256
sidecars under `dist/`. Git tags matching `v*.*.*` trigger the release workflow.

The current checksum files prove transfer integrity only. They are not publisher
authentication because the artifact and checksum come from the same source.
Signed provenance, hardened extraction, native `.deb` packaging, and lifecycle
tests are required before these artifacts are production release artifacts.

## Private repository access

The source and GitHub Releases repository, `builtbybasit/nexa-panel`, is private.
Anonymous `--download` installation and self-update cannot work. GitHub's
[release asset API](https://docs.github.com/en/rest/releases/assets) requires a
fine-grained token with read-only **Contents** permission for private assets.

For testing, create a fine-grained token limited to this one repository, place it
in a root-owned `0600` file, and pass the file path rather than putting the token
on the command line:

```bash
sudo install -m 0600 -o root -g root /path/to/token /root/nexa-release-read.token
```

The current installer persists the supplied credential at
`/etc/nexa-panel/release.token` for later update checks unless
`--no-save-token` is used. That file must remain root-owned and mode `0600`.
Define expiry, rotation, revocation, and incident response before using this on
multiple nodes. Do not use a broad classic `repo` token or a developer's normal
GitHub credential.

The v1 plan prefers a signed private package/artifact channel with per-node or
short-lived read credentials so source privacy does not require a long-lived
personal token on every server.

## Current test installation

The installer currently supports only Ubuntu 24.04. The most predictable test
path is an authenticated checkout with a locally built binary. This is not a
production recommendation; it still exercises the current host mutations and
TLS setup while the release lifecycle is being replaced:

```bash
sudo ./scripts/install.sh \
  --binary dist/nexa-linux-amd64 \
  --github-token-file /root/nexa-release-read.token \
  --panel-hostname panel.example.com \
  --tls-email operations@example.com
```

`--download` is the intended release-bundle bootstrap, but it is not a supported
v1 path yet: its custom GitHub JSON parsing and archive extraction are release
gates. Do not make production provisioning depend on it until the fixture and
lifecycle matrix in [PLAN.md](./PLAN.md) pass.

Point public DNS at the node first and allow the ACME HTTP-01 challenge to reach
port 80. The installer configures system users/directories, package repositories,
host dependencies, systemd, Nginx, and Certbot. It currently also manages UFW;
review [PLAN.md](./PLAN.md) before running it on a host with custom SSH or
firewall policy.

The installer attempts to create the first administrator in both TLS and
plaintext modes and prints the generated username/password directly to its
console, not to the install transcript. Store the password immediately. MFA is
currently optional even though parts of the UI/OpenAPI say otherwise; mandatory
admin enrollment is a v1 release gate.

If administrator seeding or readiness fails, treat the installation as failed
and do not expose it publicly. The backend writes a bootstrap token to
`/var/lib/nexa-panel/bootstrap.token`, but the current SPA cannot submit that
token for remote recovery.

### Plaintext quick-start is test-only

Running without `--panel-hostname` publishes port 8888 over cleartext HTTP and
currently enables/changes UFW rules:

```bash
sudo ./scripts/install.sh --binary dist/nexa-linux-amd64
```

Credentials and session cookies can cross the network unencrypted. Use this
only on an isolated disposable node. It is not a supported production setup.

## Self-update status

The current commands are:

```bash
sudo nexa self-update --check
sudo nexa self-update
sudo nexa self-update --version 0.2.0
sudo nexa self-update rollback
```

Private-repository checks require a valid root-only
`/etc/nexa-panel/release.token`. The token is reread for every operation, so it
can be rotated atomically without restarting the agent.

Self-update is not yet production-safe: packaging activation is incomplete,
success is reported before restart health is verified, update/rollback is not a
complete transaction, and recovery depends on the agent that was just replaced.
See [the self-update runbook](./docs/runbooks/self-update.md) for current behavior
and [PLAN.md](./PLAN.md) for the required replacement design.

## Uninstallation status

There is currently no supported uninstall or purge command. Do not improvise by
deleting `/usr/bin/nexa` or the state directories: the install also owns systemd
units/timers, Nginx, sysusers/tmpfiles, generated configuration, containers, and
credentials. A manifest-driven retain-data uninstall plus an explicit destructive
purge is a v1 release gate.

## Operations

Check services, logs, and health through the same Unix-socket topology used in
production:

```bash
sudo systemctl status nexa-agent nexa-api nginx
sudo journalctl -u nexa-agent -u nexa-api --since today
sudo curl --unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/health/live
sudo curl --unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/health/ready
sudo nexa doctor --preflight --allow-existing --json
```

The shipped `/metrics` Nginx path is currently a known release issue; verify it
after the production-topology fix rather than assuming TCP/service health proves
metrics readiness.

## Backup and disaster recovery

Panel state is split between:

- `/var/lib/nexa-panel/control.db`
- `/etc/nexa-panel/master.key`

The built-in panel-state backup intentionally puts a consistent SQLite snapshot
and its matching master key into one `nexa-panel-system.tar.gz`. That archive is
**credential-equivalent**: anyone who obtains it can decrypt every secret in the
control database. Store it only on a tightly authorized encrypted remote, keep
retention and restore access separate from normal panel users, and exercise the
restore procedure before release.

Use the documented restore command rather than manually extracting the archive;
it validates members and places the key at the current path:

- [Panel-state restore runbook](./docs/runbooks/panel-state-restore.md)
- [Self-update runbook](./docs/runbooks/self-update.md)
- [Deployment runbook](./docs/runbooks/deployments.md)
- [Audit-chain notes](./docs/security/audit-chain.md)

The HTTP contract is in [openapi/openapi.yaml](./openapi/openapi.yaml), host
requirements are in [packaging/REQUIREMENTS.md](./packaging/REQUIREMENTS.md),
and the v1 release gates are in [PLAN.md](./PLAN.md).

## License

Nexa Panel is open source under the Apache License, Version 2.0. See
[LICENSE](./LICENSE) and [NOTICE](./NOTICE), the
[security policy](./SECURITY.md), and the
[support and compatibility policy](./docs/support-policy.md).
