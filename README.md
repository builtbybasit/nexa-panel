# Nexa Panel

Nexa Panel is a **Modern Server Management Platform** built with Go and Vue 3.
It is being developed as a modular monolith with an unprivileged control plane,
a narrowly scoped privileged node agent, and independently owned feature
modules.

## Current foundation

- `nexa api`: HTTP control-plane process
- `nexa agent`: Unix-socket node-agent process
- `nexa doctor`: local capacity and Podman capability report
- System module: Linux memory-profile and Podman discovery
- Persistent SQLite control-plane state accessed through the Bun Go ORM
- First-administrator bootstrap, Argon2id passwords, hashed cookie sessions, and audit records
- Mandatory TOTP enrollment with AES-GCM encrypted seeds, replay protection, and one-time recovery codes
- Durable Bun-backed jobs with restart recovery, persisted progress events, and SSE streaming
- Safe diagnostic tracer available from the Vue Jobs page
- Viewer/operator/admin permission policy enforced on module routes
- HMAC-signed plan/apply/observe/rollback tracer over an authenticated Unix socket
- Vue plan preview, durable apply progress, guarded rollback, and audit-log views
- Independent Runtime and Sites modules with PHP-FPM 7.4 and all 8.x discovery, Bun-backed desired state, and durable Nginx/FPM plan generation
- Agent-signed site activation with Unix-user provisioning, atomic writes, PHP-FPM and Nginx validation, local Host verification, and drift-guarded rollback
- Independent Domains module for primary domains, subdomains, aliases, redirects, DNS preflight, and atomic routing updates
- Independent Certificates module for Certbot HTTP-01 issue, renewal, revocation, SAN routing, and 30-day expiry warnings
- Existing TLS remains attached during certificate planning and is restored after failed renewal or revocation, while the failure stays visible to operators
- Sites, Domains, and TLS Vue workflows with reviewed plans and durable live progress
- Independent Databases module for PostgreSQL 16, 17, and 18 instance discovery/provisioning, encrypted login roles, logical databases, scoped grants, credential rotation, verified logical backups, and staged restore
- PostgreSQL passwords stay out of plans, jobs, command arguments, results, and audit metadata; an administrator may reveal a successfully applied credential exactly once
- Independent MySQL & MariaDB module for one discovered native engine, encrypted accounts, primary-account database creation, scoped grants, rotation, verified SQL backups, and rollback-protected restore
- Real MySQL 8.4 and MariaDB 11.8 acceptance destroys and restores a database through the production operator, then reads the original fixture value
- Podman Admin Tools module for phpMyAdmin 5.2.3 and pgAdmin 9.16 with localhost-only ports, read-only roots, dropped capabilities, PID limits, and 128/256 MiB memory ceilings
- One-time Admin Tool Launch gateway keeps database credentials server-side, exchanges a 60-second launch token for a scoped HttpOnly session, strips caller identity headers, and audit-logs the launch
- Hardened container acceptance verifies phpMyAdmin signon, pgAdmin webserver authentication, the imported server catalog, and the copied pgpass entry without exposing a password in the browser URL
- Vue 3 shell: module registry, overview, and live system-capacity page
- Embedded production UI: Bun builds Vue and Go embeds it into the release binary

Podman is used for isolated administration tools such as pgAdmin. Core Nginx,
PHP-FPM, and the primary PostgreSQL instance stay native for a smaller compact
profile.

Runtime eligibility is installation-based: PHP 7.4 and every installed 8.x
branch remain selectable without an automatic end-of-support cutoff. PHP 7.4 is
reported as `end_of_life_allowed` so the UI can show its security risk.

Milestones 2, 3, and 4 in `PLAN.md` are implementation-complete. Milestone 5's
application slices are complete; a clean Ubuntu Podman/Quadlet end-to-end run,
installer engine selection, and automatic admin-tool idle shutdown remain. A production or
Let's Encrypt staging acceptance run still needs a public hostname whose DNS
points at a reachable Ubuntu node on port 80.

## Development

Requirements:

- Go 1.25+
- Bun 1.3+

Managed Ubuntu nodes additionally require Nginx, Certbot, Podman with Quadlet, one native MySQL or MariaDB server/client, each selectable
PHP-FPM branch, `postgresql-common`, `libjson-perl`, and the desired PostgreSQL
16-18 server/client packages. The agent discovers PHP only when an
`/etc/php/<version>/fpm/pool.d` directory exists and discovers PostgreSQL through
`pg_lsclusters --json`.

```bash
make test
make build
make web-install
make web-dev
NEXA_MYSQL_INTEGRATION=1 go test -run TestMySQLFamilyDestroyedDatabaseRestoreIntegration -v ./internal/platform/mysqloperator
./scripts/test-admin-tools-containers.sh
```

In a second terminal:

```bash
./bin/nexa api
```

Development state is stored in `/tmp/nexa-panel/control.db` and its generated
AES master key in `/tmp/nexa-panel/master.key`. Packaged systemd units store
both under `/var/lib/nexa-panel/`. Back up the master key separately and never
commit or expose it; encrypted TOTP seeds cannot be recovered without it.

Packaged installs keep the API-agent credential at
`/etc/nexa-panel/agent.token` with root ownership and group-read access for the
unprivileged API. Unlike the Unix socket in `/run`, this credential persists
across reboot so previously signed rollback plans remain verifiable.

Vite serves the frontend at `http://127.0.0.1:5173` and proxies `/api` to the Go
control plane at `http://127.0.0.1:8080`.

Create the single-binary production build with:

```bash
make release
```

This runs the Vue build through Bun, writes generated assets to the Go web UI
package, and compiles them using the `embed` build tag. Bun and Node.js are not
required on the production server.

Build the tested Linux AMD64 artifact and checksum with:

```bash
make release-linux
```

The initial HTTP contract is documented in
[openapi/openapi.yaml](./openapi/openapi.yaml). Packaging assets for the separate
unprivileged API and privileged agent roles live under `packaging/`.

On Linux, inspect the local node with:

```bash
./bin/nexa doctor
```

See [PLAN.md](./PLAN.md) for the complete product and delivery plan.
