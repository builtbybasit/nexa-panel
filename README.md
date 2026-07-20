# Nexa Panel

Nexa Panel is a pre-release server-management platform for Ubuntu 24.04 LTS.
It uses a Go control plane, a narrowly scoped privileged node agent, and a Vue
3 frontend. The project is still under active testing and is not yet a stable
release.

## Architecture

- The unprivileged `nexa api` process owns authentication, authorization,
  durable jobs, audit records, desired state, and the HTTP API.
- The root `nexa agent` process exposes authenticated operations over a local
  Unix socket. Configuration workflows use signed
  plan/apply/observe/rollback flows.
- Feature modules own sites, domains, certificates, PostgreSQL, MySQL/MariaDB,
  files, logs, schedules, backups, applications, and containerized database
  administration tools.
- SQLite stores control-plane state. Long-running work is persisted and resumes
  through the durable job worker after a process restart.
- Vue 3 and Bun build the web application. Production assets are embedded in a
  single Go binary.

The packaged API listens on `/run/nexa-panel/api.sock`; Nginx is the only public
ingress. The privileged agent listens separately on
`/run/nexa-panel/agent.sock`.

Podman is used only for isolated administration tools such as phpMyAdmin and
pgAdmin. Nginx, PHP-FPM, PostgreSQL, and MySQL/MariaDB remain native services on
the managed node.

## Development

Requirements:

- Go 1.25.12 or newer (the minimum toolchain is pinned in `go.mod`)
- Bun 1.3.9 (the version pinned in `web/package.json`)
- Linux or macOS for local development

Install the locked frontend dependencies and run every required quality gate:

```bash
make check
```

`make check` performs the frozen Bun install, then verifies Go formatting and
module tidiness, vetting, Staticcheck, production dead-code reachability,
govulncheck, and tests. It also checks shell scripts, runs frontend tests and
type checking, builds the production frontend, audits both the normal and
production frontend graphs with Knip, checks dependency advisories, and
validates the OpenAPI contract. `make ci` additionally runs the Go race
detector and compiles the production-only embedded-asset path.

For local development, build the Go binary and run the agent and API in separate
terminals:

```bash
make build
./bin/nexa agent
```

```bash
./bin/nexa api
```

Then start Vite in a third terminal:

```bash
make web-dev
```

Vite serves `http://127.0.0.1:5173` and proxies `/api` to
`http://127.0.0.1:8080`. Development state, the master key, the agent token,
and Unix sockets default to `/tmp/nexa-panel/`.

The opt-in MySQL 8.4 and MariaDB 11.8 destructive restore acceptance test needs
Docker:

```bash
NEXA_MYSQL_INTEGRATION=1 go test -run TestMySQLFamilyDestroyedDatabaseRestoreIntegration -v ./internal/platform/operators/mysql
```

## Production build and installation

Build and validate a Linux AMD64 release with a SHA-256 checksum:

```bash
make release-linux
```

For ARM64, run `bash scripts/build-linux-release.sh arm64`. The build script
runs the same checks as CI before writing the binary and checksum under `dist/`.

The installer supports Ubuntu 24.04 LTS only. It configures the system users,
directories, systemd services, Nginx proxy, required host packages, and the PHP
and PostgreSQL package repositories. From a source checkout on the target node:

```bash
sudo ./scripts/install.sh \
  --binary dist/nexa-linux-amd64 \
  --panel-hostname panel.example.com \
  --tls-email operations@example.com
```

Before requesting a certificate, point the hostname's public DNS at the node
and allow inbound HTTP on port 80. Certbot uses that listener for the initial
HTTP-01 challenge and configures the HTTPS redirect.

Without `--panel-hostname`, the installer exposes a bootstrap listener only on
`127.0.0.1:8080`. Reach it through an SSH tunnel instead of publishing that
listener directly:

```bash
ssh -L 8080:127.0.0.1:8080 administrator@server
```

Then open `http://127.0.0.1:8080`. The first account becomes the administrator
and must finish TOTP enrollment before entering the panel. Store its one-time
recovery codes outside the managed node.

The privileged systemd/container setup in `Dockerfile` and `compose.yaml` is a
disposable Ubuntu acceptance node, not a supported production deployment.

## Operations and security

Inspect the services and recent logs with:

```bash
sudo systemctl status nexa-agent nexa-api nginx
sudo journalctl -u nexa-agent -u nexa-api --since today
sudo curl --unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/health/live
sudo curl --unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/health/ready
```

The API runs as the unprivileged `nexa` account. The root agent is confined by
systemd hardening and an explicit writable-path boundary. Requests to the agent
require the root-owned credential at `/etc/nexa-panel/agent.token`. The agent's
systemd preflight creates or validates that credential before startup, and the
API receives a private systemd credential copy. Role and site-scope checks are
enforced server-side; sensitive operations additionally require recent MFA.

Persistent state is stored in `/var/lib/nexa-panel/control.db`, and encrypted
secrets depend on `/var/lib/nexa-panel/master.key`. Back up both on the same
recovery cadence, store the key in a separate restricted location, and never
commit or expose either file. Encrypted TOTP seeds and managed database
credentials cannot be recovered without the master key.

The HTTP contract is documented in
[openapi/openapi.yaml](./openapi/openapi.yaml), host-level dependencies in
[packaging/REQUIREMENTS.md](./packaging/REQUIREMENTS.md), and the product plan
in [PLAN.md](./PLAN.md).
