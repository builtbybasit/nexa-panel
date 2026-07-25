# Nexa Panel v1 Release Plan

> Status: **release blocked**
>
> Updated: 2026-07-23
>
> Target: the first production release for Ubuntu 24.04 LTS on AMD64 and ARM64
>
> First live-node qualification ran on 2026-07-23 against a real Ubuntu 24.04
> AMD64 server with a real DNS name and a real Let's Encrypt certificate. It
> found **seven release-blocking defects in one session**, none of which the
> container node, the Go suite, or the shell contract tests could reach. Six are
> fixed; one is open. See section 10.

This document replaces the original implementation roadmap. Most of the product
surface now exists; the remaining work is release hardening. A version must not
be called production-ready merely because it builds or works on the disposable
Docker node. Version 1 is ready only when installation, upgrade, rollback,
uninstallation, recovery, and the operator-critical UI journeys pass the
acceptance matrix in this plan.

## 1. Release decision

The current tree must not be released to an Internet-facing server yet. The
highest-risk blockers are:

1. An Nginx compromise can reach the root agent and turn the local-binary update
   RPC into root code execution.
2. Updates are not a single atomic transaction and can report success before
   packaging is activated or the restarted services are healthy.
3. Rollback depends on the newly installed agent and is incomplete for the first
   update, host packages, removed files, and database migrations.
4. There is no supported uninstall or purge path.
5. Release checksums are not authenticity proof, and the root bootstrap extracts
   an untrusted tar archive without a hardened extraction policy.
6. The private GitHub repository requires a deliberate credential and artifact
   distribution design; anonymous installation and updates cannot work.
7. Administrators can use privileged operations without enrolling MFA.
8. Several UI actions can lock an operator out or leak one user's cached data to
   the next signed-in user.

No release tag should be published until every item marked **Release gate** below
has an automated regression test and an operator recovery procedure.

## 2. Product baseline already implemented

The following capabilities are present in the current codebase. “Implemented”
does not mean release-accepted; gaps are listed in later sections.

| Area | Current implementation | v1 qualification still needed |
| --- | --- | --- |
| Control panel | Go modular monolith, SQLite/WAL state, embedded migrations, encrypted secrets, Unix-socket HTTP | Contract generation, migration rollback policy, production topology tests |
| Privileged execution | Separate root agent, authenticated Unix socket, plan/apply/observe flows, hardened systemd units | Split API/Nginx trust from agent trust; remove root-equivalent local update RPC exposure |
| Identity | Password sessions, CSRF/origin checks, RBAC, site scopes, TOTP and recovery codes | Mandatory admin MFA, break-glass recovery, truthful OpenAPI/UI policy |
| Jobs and audit | Durable jobs, restart recovery, progress streams, audit hash chain | Redacted display DTOs and complete audit-sink use for destructive file operations |
| Sites | Site lifecycle, domains, certificates, PHP runtime, file manager/editor, deployments, SFTP | Complete ownership graph and teardown of schedules/backup references |
| Databases | PostgreSQL and MySQL/MariaDB lifecycle, users, grants, backup/restore | Required real-engine acceptance suites in CI and shared orchestration seams |
| Scheduling | Site tasks, backup plans, generated systemd/cron artifacts | One canonical cron grammar and relational cleanup on site deletion |
| Backups | Site/database copies, remote accounts, retention, panel-state backup and restore | Restore drills, key/archive threat model in docs, schema compatibility tests |
| Applications | Package catalog, PHP/PostgreSQL versions, phpMyAdmin, containerized pgAdmin | pgAdmin proxy/session regression, readiness and stop-policy acceptance |
| Host operations | Firewall, service control, logs, system status, doctor/preflight | Lockout prevention, transactional firewall/service changes, metrics topology fix |
| Frontend | Vue 3, Pinia, TanStack Query, lazy feature routes, plan review, shared job UI, Monaco editor | Session isolation, central 401 handling, E2E/a11y coverage, safe destructive flows |
| Release tooling | AMD64/ARM64 embedded binaries, tar bundles, SHA-256 sidecars, pinned actions | Signed provenance, native package, lifecycle CI, SBOM, changelog and support policy |
| Self-update | Private GitHub release lookup, token file, archive validation, packaging retention, CLI/UI entry points | Atomic activation, independent recovery, health-gated success, token rotation model |

Current automated strengths include Go unit/integration-style tests, race tests,
Staticcheck, dead-code checks, govulncheck, shell syntax/ShellCheck, Vitest,
TypeScript checking, Knip, OpenAPI linting, and an embedded production build.

## 3. Version 1 release gates

### SEC-001 — Separate public ingress from root-agent trust

**Severity: P0 / Release gate**

The installer adds `www-data` to the `nexa` group. That group can read the agent
bearer token and connect to both panel sockets. The agent accepts a caller-owned
absolute `BinaryPath`, reads it, stages it under `/usr/bin`, and executes it as
root to inspect its version. A compromised Nginx worker can therefore become
root.

Done means:

- API and agent sockets use different groups and directories.
- Nginx can connect only to the API socket and cannot read the agent token.
- `BinaryPath` is removed from the HTTP/RPC surface. A local push, if retained,
  is a root-only offline CLI that copies from root-owned staging using
  race-resistant ownership and symlink checks.
- An automated negative test proves the Nginx identity cannot connect to the
  agent or read any agent credential.

### LIF-001 — Make update one recoverable transaction

**Severity: P0 / Release gate**

The binary is currently replaced before packaging succeeds. Packaging failure,
archive-retention failure, cancellation, process death, or power loss can leave
mixed versions. The update must use a journaled state machine, not a sequence of
best-effort renames.

Done means:

- A host-level lock serializes UI, CLI, installer, and recovery operations.
- All artifacts, packaging, configuration, and migrations are validated while
  staged before the live version changes.
- Versioned release directories are activated by one atomic pointer change.
- The transaction and every filesystem rename are durably journaled and fsynced.
- Cancellation does not terminate `apt` or `dpkg` in the middle of a database
  transaction; package-manager recovery is explicit.
- Every injected failure returns the host to the old healthy version.

### LIF-002 — Activate packaging and health-check before success

**Severity: P0 / Release gate**

`--sync-packaging --no-start` does not run `systemctl daemon-reload` or reload
Nginx. The detached restart handles only the API and agent, and the job is marked
successful before restart health is known. Certbot-managed TLS vhosts are skipped,
so proxy/security fixes cannot currently reach those nodes.

Done means:

- Activation validates Nginx, reloads systemd, applies managed proxy changes,
  restarts in dependency order, and verifies API socket, agent socket, Nginx,
  migrations, version, and public readiness.
- Success is recorded only after all health checks pass.
- Failed activation automatically restores the prior complete release and
  verifies that recovery.
- TLS publishing state is represented as managed data, not reverse-engineered
  from a Certbot-mutated file.

### LIF-003 — Provide recovery independent of the new agent

**Severity: P0 / Release gate**

The supported rollback command is an RPC to the agent that may have just failed
to start. Packaging rollback also cannot remove new files, undo host-package
changes, or guarantee the previous binary can read a migrated database.

Done means:

- A root-only offline rescue command works while both panel services are down.
- The first production update is as reversible as every later update.
- Rollback restores the complete owned-file manifest and publishing config.
- Migration policy is expand/contract or otherwise proves N-1 binary
  compatibility; irreversible migrations block rollback before activation.
- A systemd failure path can select the previous release without the panel API.

### INS-001 — Make installation fail-safe and TLS-first

**Severity: P0 / Release gate**

The installer replaces the binary before preflight, defaults to public plaintext
port 8888, changes UFW policy, assumes SSH port 22, and can exit successfully
while readiness or administrator seeding failed. Re-running without hostname
flags can replace an existing TLS publication with the insecure quick-start.

Done means:

- Production mode requires a hostname and TLS (or an explicit, documented
  external-TLS mode). Plaintext is an explicit test-only flag.
- Preflight and a complete dry-run plan occur before any host mutation.
- Existing SSH/firewall/Nginx policy is discovered and preserved; no global UFW
  enablement occurs without explicit confirmation and a rollback timer.
- Managed writes are atomic, mode/owner drift is corrected, and removed files
  are reconciled from an ownership manifest.
- The installer waits for all services, public ingress, migrations, and first
  administrator creation; any failure exits nonzero and rolls back.
- Re-running with no publishing flags preserves the current publishing mode.
- The GitHub metadata parser handles valid JSON, whitespace, field ordering, and
  error bodies with fixture coverage.

### DIST-001 — Define private release distribution and credential lifecycle

**Severity: P0 / Release gate**

`builtbybasit/nexa-panel` is private. GitHub release metadata and assets require
authentication, and the current Docker test node confirms `self-update --check`
fails when `/etc/nexa-panel/release.token` is absent. Persisting one long-lived
personal token on every root-managed host creates a fleet-wide rotation and
blast-radius problem.

Preferred production design:

1. Keep source private but publish signed packages to a dedicated private APT
   repository or artifact store.
2. Give each node a revocable, read-only, repository-specific credential or a
   short-lived signed download URL.
3. Keep the signing trust root independent of the download credential.

If GitHub Releases remains the v1 source:

- Use a fine-grained token scoped to this one repository with read-only Contents
  permission; never use a developer's broad classic `repo` token.
- Define creation, installation, expiry, rotation, revocation, backup exclusion,
  and incident-response procedures.
- Prefer `--github-token-file`; never print a token, pass it in argv, put it in a
  job payload, or expose it to the API/Nginx identities.
- Test missing, expired, revoked, rate-limited, and insufficient-scope tokens.

### SUP-001 — Authenticate every root-executed artifact

**Severity: P0 / Release gate**

A checksum downloaded beside an artifact detects transfer corruption but does
not prove who published either file. The installer then extracts the archive as
root without rejecting traversal, symlinks, devices, duplicate members, or
expanded-size abuse.

Done means:

- CI produces signed build provenance and an SBOM for every artifact.
- Installer and updater verify signature/provenance against a pinned identity or
  public key before extraction or execution.
- Extraction rejects absolute/traversing names, links, devices, duplicate paths,
  wrong layout, excessive member count, and excessive expanded size.
- Tag, embedded version, release metadata, architecture, digest, and signature
  must agree.

### UNS-001 — Ship idempotent uninstall and explicit purge

**Severity: P0 / Release gate**

There is no uninstall command even though installation changes systemd,
sysusers/tmpfiles, Nginx, UFW, SSH, package repositories, groups, timers,
containers, secrets, and persistent data.

Done means:

- `nexa uninstall --dry-run` prints the exact ownership-manifest plan.
- Default uninstall removes program-owned binaries/config/units and stops all
  managed processes while preserving sites, databases, backups, control state,
  master key, and release credential.
- `--purge-data` is a separate typed/interactive confirmation and lists every
  destructive path before deletion.
- Partial installs, repeated uninstall, failed prior upgrades, active admin
  tools, timers, and reboot are covered.
- systemd and Nginx are reloaded and the host is left boot-clean.

### ID-001 — Enforce administrator MFA consistently

**Severity: P1 / Release gate**

Runtime authorization currently skips MFA step-up for an administrator who
never enrolled, while OpenAPI and UI copy claim enrollment is mandatory.

Done means:

- An administrator cannot reach privileged capabilities until enrollment is
  confirmed.
- Recovery codes, reset, support-safe break-glass recovery, and lost-device flow
  are tested.
- Backend behavior, OpenAPI, frontend state, and copy share one policy.
- Password requirements are returned by the server and rendered exactly.

### DATA-001 — Make site teardown ownership-complete

**Severity: P1 / Release gate**

Schedules and backup-plan targets can reference a site without relational
cascade or teardown coordination. Deleting the site/Unix user can leave root
scheduled work targeting a deleted identity.

Done means:

- One ownership graph lists domains, certificates, deployments, SFTP, databases,
  schedules, backup plans/copies, generated files, and host users.
- Teardown blocks or plans every dependent resource before host deletion.
- Relational constraints/cascades are used where appropriate; IDs are not hidden
  in unqueryable JSON when lifecycle depends on them.
- Repeated and interrupted teardown converges safely.

### UI-001 — Isolate sessions and handle expiry globally

**Severity: P1 / Release gate**

TanStack Query uses a global client, while logout clears only the Pinia identity
store. A second account can receive cached data from the first. HTTP 401 is also
handled as an ordinary page error, leaving polling and the authenticated shell
running.

Done means:

- Login, logout, account switch, and session expiry cancel requests, stop job
  streams, and clear every user-scoped cache.
- A central 401 path returns to the auth gate once and preserves no protected UI.
- Automated two-account and expiry tests prove isolation.

### UI-002 — Prevent operator lockout and accidental data loss

**Severity: P1 / Release gate**

The UI can immediately stop Nginx, remove active SSH/panel firewall rules, lose
dirty editor state on navigation, and restore backups destructively without a
reviewed dry-run.

Done means:

- Panel-critical services and active access rules cannot be removed without a
  verified alternative and timed rollback.
- Destructive operations show impact, require confirmation proportional to risk,
  and remain recoverable if the browser loses contact.
- Dirty editor state is guarded across site switch, route change, logout, reload,
  and browser close.
- Restore rejects unknown archive types and requires plan/dry-run review plus
  typed confirmation for overwrite/clear.

### UI-003 — Complete first-run and update journeys

**Severity: P1 / Release gate**

The backend tells remote setup clients when a bootstrap token is required, but
the SPA neither renders nor submits that token. This makes browser recovery
impossible if installer seeding fails. The update UI also promises reconnection
but marks the first transport loss as failure.

Done means:

- Installer seeding is transactional, and the SPA also supports the documented
  bootstrap-token contract as a recovery path.
- Update progress survives API/agent restart, distinguishes expected transport
  loss, reconnects, and displays final version/health or rollback result.
- The Overview “Create site” shortcut uses the canonical `/sites/new` route.

### ADM-001 — Prove admin-tool proxy sessions

**Severity: P1 / Release gate**

The live `nexa-node` journal showed pgAdmin dashboard and cleanup requests
returning HTTP 401 seconds after a successful launch. The basic services were
healthy, so this needs an authenticated browser/proxy regression rather than a
container-start test.

Done means:

- Launch, assets, redirects, cookies, XHR/fetch, WebSocket use (if any), idle
  timeout, relaunch, and automatic stop pass through `/tools/pgadmin/`.
- phpMyAdmin receives the same regression coverage.
- Tool sessions are isolated per panel user and never expose database secrets to
  browser-visible URLs, storage, or logs.

### OPS-001 — Test the real lifecycle on clean virtual machines

**Severity: P1 / Release gate**

Current release CI validates archive layout, not a real host lifecycle. The
Docker node is useful for smoke testing but mounts `/usr/bin/nexa` read-only,
disables important agent hardening, and discards state, so it cannot prove
production update or rollback behavior.

Required CI matrix:

| Scenario | AMD64 | ARM64 | Required assertions |
| --- | --- | --- | --- |
| Fresh TLS install | yes | yes | preflight, service/public readiness, admin creation, reboot |
| Idempotent re-run | yes | yes | no publishing or permission drift |
| Failed preflight | yes | yes | zero host mutations |
| N-1 to N update | yes | yes | signature, migration, activation, health, version |
| Injected update failure | yes | yes | automatic complete rollback |
| Offline rollback | yes | yes | works with API/agent stopped |
| Uninstall retain-data | yes | yes | owned code removed, customer data intact |
| Reinstall after retain | yes | yes | state and secrets recover correctly |
| Purge uninstall | yes | yes | no owned processes/files/data remain |
| MySQL/MariaDB restore | yes | at least smoke | destructive restore acceptance enabled |
| PostgreSQL restore | yes | at least smoke | destructive restore acceptance enabled |

## 4. Important production improvements

These are not substitutes for the gates above, but should be closed before or
immediately after the first release candidate.

- Fix `/metrics` through the packaged Nginx topology. A local request on
  `nexa-node` currently returns 404 because the control panel cannot recognize
  the Unix-socket proxy peer as loopback and the metrics location does not send
  the trusted local forwarding signal.
- Generate or validate server/client types from OpenAPI. The current schema
  omits the real `developer` role and disagrees with runtime MFA behavior.
- Route every sensitive file operation through the audit sink and define whether
  audit persistence is fail-closed or explicitly logged best-effort.
- Add route-level frontend permission guards and a deliberate 403 view. Backend
  authorization remains authoritative.
- Gate command-palette queries by capability instead of issuing unauthorized
  requests for every resource type.
- Replace generic job request/result JSON rendering with redacted display DTOs.
- Make ShellCheck mandatory in CI rather than optional when installed.
- Add `LICENSE` or private commercial license terms, `SECURITY.md`, `CHANGELOG.md`,
  supported-version/upgrade policy, and a vulnerability-response process.
- Define database schema compatibility, supported N-1 upgrade window, and backup
  restore guarantees.

## 5. Maintainability and scalability work

### Deepen module boundaries

- Keep the control panel/agent split, but make privileged interfaces narrow and
  semantic. Callers request “activate verified release X,” never “read and run
  this path.”
- Extract the common MySQL/PostgreSQL lifecycle shell—list/detail/status,
  credential reveal, job/plan wiring, and table components—behind small engine
  adapters. Keep engine SQL and host operators separate.
- Split `FilesView.vue` (about 1,478 lines), `DatabasesView.vue` (about 1,097),
  and `MySQLView.vue` (about 991) into browser/session/action/dialog modules with
  explicit state ownership.
- Replace the 964-line shell installer with a small authenticated bootstrap plus
  a compiled install plan/apply/recover/uninstall module or native package
  lifecycle. Root OS transactions still need project-owned policy; no library
  can make them atomic automatically.
- Consolidate the multiple Go and TypeScript cron parsers behind one defined
  five-field grammar and a shared conformance corpus.

### Maintained packages and tools worth adopting

| Candidate | Decision | Code/debt it should replace |
| --- | --- | --- |
| GoReleaser + nFPM | Adopt for v1 packaging | Hand-built tar/release assembly and much of installer-owned file placement; produce versioned `.deb`, checksums, metadata, and SBOM |
| `golang.org/x/mod/semver` | Adopt | Handwritten version normalization/comparison and its edge-case tests |
| `oapi-codegen` | Adopt incrementally | Handwritten Go request/response DTO drift and route contract glue |
| `openapi-typescript` + `openapi-fetch` | Adopt after the contract is truthful | Repeated frontend endpoint types/request wrappers; keep CSRF, MFA, and global 401 behavior as middleware |
| `bats-core` | Adopt | Ad-hoc shell-only checking; add injected-failure install/update/uninstall tests |
| Playwright + `@axe-core/playwright` | Adopt | Missing critical-journey, browser-session, responsive, and accessibility coverage |
| TanStack Vue Table v8 | Evaluate on repeated resource tables | Repeated sorting, selection, pagination, responsive-column, and URL-state code while preserving project markup |
| VeeValidate v5 + Valibot | Evaluate on complex forms | Repeated refs, validation, error mapping, and payload construction; server validation remains authoritative |
| `cron-parser` | Evaluate for frontend preview/next-runs | The 288-line frontend cron evaluator; pin the accepted five-field grammar and test it against the backend |
| `coreos/go-systemd/v22/unit` | Evaluate where output matches exactly | Handwritten systemd escaping and unit serialization |

Do not add Axios, a second global state framework, or a generic “service layer.”
The native Fetch API plus generated types is enough. Do not adopt a dependency
only to reduce line count: it must have active maintenance, a compatible license,
a narrow security surface, pinned versions, and a removal plan.

## 6. UI and UX release plan

### Release-critical UX

1. Session expiry and account switching must be immediate, global, and free of
   stale protected content.
2. Setup must render server-provided password policy and bootstrap-token needs.
3. MFA enrollment, challenge, recovery, and privileged step-up must use one
   coherent policy and copy.
4. Service/firewall/update/restore actions must show blast radius, recovery path,
   and reviewed plan before execution.
5. Long operations must survive navigation and expected service restarts without
   being mislabeled as failures.
6. Editor, deployment, and restore flows must never discard unsaved or existing
   data silently.

### Accessibility and responsive design

- Give every field a stable `id`, label, description, and error relationship;
  do not wrap custom combobox buttons in an implicit `<label>`.
- Replace nested pseudo-buttons with real keyboard-focusable controls.
- Add skip navigation, route focus management, visible focus, reduced motion,
  dialog/drawer focus traps, and inert mobile-menu background.
- Render dense tables as scrollable tables with sticky identity/actions or mobile
  cards. Turn the fixed file sidebar into a drawer at narrow widths.
- Add automated WCAG A/AA scans, keyboard journeys, and manual screen-reader
  checks; automation alone is not an accessibility sign-off.

### Performance

- Keep Monaco lazy. The current build emits a roughly 3.9 MB minified Monaco
  worker bundle (about 1 MB gzip) plus a large TypeScript worker; load only the
  languages/features required for server files.
- Set route and editor bundle budgets and fail CI on material regressions.
- Keep feature routes lazy and avoid eager all-icon imports.

## 7. Verification baseline from the 2026-07-22 review

Passed against the implementation state now recorded at `e825022`
(documentation edits excluded):

- Go tests across all packages (892 tests reported by the suite)
- Go race suite
- Embedded-frontend Go suite
- `go vet`, `go mod tidy -diff`, format check, Staticcheck, Go dead-code scan
- `govulncheck`: no reachable vulnerabilities
- Shell syntax and ShellCheck
- Frontend tests: 29 files / 169 tests
- TypeScript checking, production Vite build, Knip normal/production scans
- OpenAPI lint
- `nexa-node`: API/agent/Nginx active, no failed units, live/ready healthy,
  `nginx -t`, systemd unit verification, and doctor/preflight pass

Not yet proven:

- Frontend advisory lookup was not run in this review because it sends the
  dependency manifest to an external advisory service without explicit approval.
- Opt-in destructive MySQL/MariaDB and PostgreSQL acceptance suites were not run.
- No fresh TLS VM install, real N-1 update, rollback, interrupted update,
  retain-data uninstall, purge, reboot, or restore drill exists in CI.
- No live AMD64 node was exercised.
- The in-app visual browser could not attach; the UX findings come from source,
  tests, production build output, and the live node journal.

## 8. Delivery sequence

### Phase 0 — Contain the privilege boundary

- Close SEC-001 first.
- Add the negative Nginx/agent identity test.
- Freeze release tagging until it passes.

### Phase 1 — Replace the lifecycle foundation

- Choose signed `.deb` plus private artifact distribution.
- Implement ownership manifest, transactional install/update/recovery/uninstall,
  private credential rotation, signature verification, and hardened extraction.
- Add lifecycle fault-injection tests before adding more update UI.

### Phase 2 — Close correctness gates

- Enforce MFA and align OpenAPI/frontend policy.
- Make site teardown ownership-complete.
- Fix metrics, pgAdmin proxy sessions, bootstrap recovery, and update reconnect.

### Phase 3 — Operator safety and UX

- Central session invalidation and query-cache isolation.
- Lockout-safe firewall/service actions, restore planning, dirty-editor guards,
  permission routing, accessibility, and responsive tables.

### Phase 4 — Release candidate qualification

- Run the full VM matrix on AMD64 and ARM64.
- Perform backup/restore and offline rollback drills.
- Produce SBOM, signed provenance, changelog, release notes, and support policy.
- Run a 7-day canary on a noncritical live node with monitoring and an exercised
  recovery path.

## 9. Definition of done for v1.0.0

Version 1 is releasable only when all of the following are true:

- Every release gate in section 3 is closed with automated evidence.
- Install/update/rollback/uninstall are idempotent and fault-injection tested.
- A private-repository credential can be rotated or revoked without reinstalling.
- A compromised Nginx/PHP/site process cannot reach a root-capable interface.
- No update is marked successful before public readiness and version checks pass.
- N-1 rollback is compatible with every v1 migration.
- Default installation is encrypted and cannot silently replace existing host
  firewall, SSH, or Nginx policy.
- The two real database acceptance suites and browser critical journeys run in CI.
- Backup restore and offline recovery have been performed from the published
  artifacts, not a source checkout.
- Documentation matches observed behavior, and release artifacts are signed.

Until then, `nexa-node` and plaintext port 8888 remain disposable test paths,
not supported production deployment methods.

## 10. Live-node qualification, 2026-07-23

First execution of the lifecycle on real hardware: Linode Ubuntu 24.04.4 AMD64,
2 GiB / 1 vCPU, hostname `panjnadvetclinic.com` with a real Let's Encrypt
certificate. AMD64 had never been exercised anywhere, and no TLS-published node
had ever existed.

### 10.1 Defects found

Seven release blockers in one session. Every one was invisible to the container
node, the Go suite, and the shell contract tests, because each needs a condition
the disposable node cannot produce: real TLS publishing, a real interrupt, a real
prior install, a real reboot, or a real bad artifact.

| # | Defect | Consequence | Status |
| --- | --- | --- | --- |
| 1 | The API refused its own Unix socket as insecure transport (426). `RemoteAddr` is `@` on a socket, so `requireSecureTransport` judged the local seed helper a public cleartext client. | **No TLS install could ever complete.** Certbot issued, HTTPS answered 200, then seeding failed and the installer rolled back a working panel. | Fixed — `httpapi.IsLocalSocketCaller`; a forwarded client is still refused. |
| 2 | `install.sh` trapped only `EXIT`; bash does not run it on an untrapped `SIGTERM`. | An interrupted install skipped rollback and left a running but **unseeded** panel with no administrator and no way in. | Fixed — `trap … INT TERM HUP`. |
| 3 | A validated ownership marker proves the run upgrades our own install, but preflight was still invoked without `--allow-existing`. | **Every re-run on a live host failed**, the opposite of the idempotent re-run the installer promises. | Fixed — a validated marker implies `--allow-existing`. |
| 4 | The SPA fallback served `index.html` with HTTP 200 for unmatched `/api/*` paths. | A typo, a removed endpoint, and a version mismatch were **indistinguishable from success** to any JSON client. | Fixed — JSON 404 for unrouted API paths. |
| 5 | `nexa-agent` listed `/run/containers` in `ReadWritePaths`, which Podman creates lazily, so the first boot died with `226/NAMESPACE`. The agent recovered on its own restart, but `nexa-api` used `Requires=`, and systemd never retries a job cancelled by a failed `Requires=` dependency. | **The panel was dead after every reboot** — with zero failed units, so nothing looked wrong — until an operator logged in and started it by hand. | Fixed — `-` prefix on lazily-created `/run` trees; `Wants=` with `After=`. |
| 6 | `validateBinary` accepted any candidate whose `version` exited zero, and the activation helper *is* the newly installed binary, so a candidate that runs without being nexa never advances the journal; `waitForActivation` then expired and returned, leaving the bad binary live. | **Bricked the node.** A 22-byte shell script was installed as `/usr/bin/nexa`; the panel survived only because the running processes held the deleted inode. | Fixed — version-shape validation, and a stalled activation now restores itself. |
| 7 | Uninstall removes the panel vhost, which is the only source of publishing truth. A reinstall over retained state therefore cannot recover the publishing mode. | **Reinstall silently downgraded a public HTTPS node to loopback-only `127.0.0.1:8888`.** The certificate was retained but unused; the panel became unreachable from the internet with no error. | Fixed — the installer writes and reads `/etc/nexa-panel/publishing.json`; see 10.3. |
| 8 | `nexa-agent.service` named `User=root` alongside `NoNewPrivileges=true`. Naming the user explicitly makes systemd withhold `CAP_SETUID`, and `NoNewPrivileges` makes it unregainable; apt's http/https methods `seteuid` to `_apt` (uid 42) before fetching. | **No application, PHP extension or database engine could ever be installed from the panel**, on any node. Every apt download died with `E: seteuid 42 failed`. Hidden because `install.sh` does its own apt work as a plain root script, never under the unit, and catalog enumeration uses `apt-cache`, which needs no privilege drop — so the UI listed versions correctly right up to the moment of install. | Fixed — the redundant `User=root` is gone; see 10.5. |
| 9 | Activation restarts `nexa-agent`, which severs the apply request the agent is still serving, so `nexa self-update` reported `Post "http://unix/v1/self-update/apply": EOF` — a failure — on an update that had already committed. The same restart also expired the agent's 5 s shutdown drain, so it exited 1 and systemd marked the unit failed on every successful update. | **Every successful update was reported as a failed one.** The two obvious operator reactions to that report, retrying and starting recovery, are both wrong on a node that updated correctly. | Fixed — the client resumes from the durable journal over a new `GET /v1/self-update/transaction` route; the agent exits 0 when its drain is outlived. See 10.6. |
| 10 | The vendor-repository setup script runs `gpg`, and `nexa-agent.service` sets `ProtectHome=true`, so `$HOME/.gnupg` cannot be created. Every `gpg` call died with `Fatal: can't create directory '/root/.gnupg'` before reading anything, which emptied the fingerprint variable. | **No MySQL or MariaDB series from a vendor repository could be installed on any node**, and the error blamed the vendor: `signing key fingerprint mismatch: downloaded none, pinned 177F…`. The pin was fine; gpg had never run. | Fixed — the script exports a private `GNUPGHOME` inside its own work directory rather than widening the unit's sandbox. See 10.7. |
| 11 | `add-apt-repository ppa:ondrej/php` resolves the PPA through launchpadlib, which caches into `$HOME/.launchpadlib`. With `ProtectHome=true` that is `/root`, read-only, so it aborted with `OSError: [Errno 30] Read-only file system` before it ever reached the network. | **No PHP version could be installed from the panel on any node.** Found within minutes of defect 10 being fixed, by installing PHP 8.5 — the exact follow-up 10.7 said was unproven. | Fixed — repository tooling is handed a writable `HOME` under `/var/lib/nexa-panel-apt`. See 10.8. |

### 10.2 Proven working on real hardware

- Bundle checksum, extraction, and layout.
- `--dry-run` prints the complete plan — packages, repositories, files, units,
  services, publishing — and makes **zero** host mutations (verified by diffing
  the host before and after).
- Preflight blocks below the 2 GiB minimum and passes above it; a refused
  preflight mutates nothing.
- Real Let's Encrypt issuance and deployment for a real DNS name.
- Public-ingress verification through the published listener before success.
- Administrator seeding, and login over public HTTPS returning
  `next: authenticated` — the optional-MFA policy behaves as intended.
- Complete ordered installer rollback that correctly **retains** the TLS
  certificate, so a retry does not burn Let's Encrypt rate limits.
- Operator-lockout protection end to end: an unacknowledged stop of a
  panel-critical service is refused `409 lockout_risk`; an acknowledged stop took
  nginx down and the backend **restored it automatically at ~117 s**, with public
  HTTPS returning to 200. The revert is backend-enforced and survives the client
  disconnecting.
- Reboot recovery: panel healthy and serving immediately after boot, zero failed
  units (after defect 5 was fixed).
- Self-update 0.1.0-dev → 0.2.0 → rollback → 0.3.0, with readiness reporting the
  expected version at each step.
- **Offline rollback with both services stopped**, which also restarted them.
- Site provisioning: dedicated `nexa_testsite` system user, `0750
  nexa_testsite:www-data` document root, per-site FPM socket, serving PHP 8.3.32.
- Retain-data uninstall: panel binaries and units removed, services stopped,
  site data byte-identical, `control.db` retained, nginx still valid.

Resource use at idle: `nexa-api` 73 MB, `nexa-agent` 6 MB, against caps of 512 MB
and 1.5 GB.

### 10.3 Fixed: publishing state is recorded, not inferred

Defect 7 was the concrete failure of the LIF-002 item "TLS publishing state is
represented as managed data, not reverse-engineered from a Certbot-mutated file."
The `internal/platform/publishing` package and the `nexa publishing
show|set|migrate` CLI existed, but nothing wired them into the install path:
`install.sh` decided TLS by grepping the vhost for `managed by Certbot`, and
uninstall removes that vhost, so the publication could not survive an
uninstall/reinstall cycle.

What landed:

- `scripts/install.sh` records the publication — hostname, listen address, port,
  TLS, external-TLS — in `/etc/nexa-panel/publishing.json` at the moment it
  renders the vhost, describing where the listener *lands* (a TLS install records
  :443, which is where Certbot moves it). It reads that record back in preference
  to inspecting the vhost, through `nexa publishing show --shell`.
- An existing publication is now preserved when **either** the record or the
  vhost is present, so a retain-data uninstall no longer looks like a fresh
  machine. When the record outlived the vhost, the vhost is re-rendered from it
  and `certbot install --nginx --cert-name HOST --redirect` re-deploys the
  retained certificate — no issuance, so no rate limit is spent.
- The cleartext opt-in travels with the publication rather than being re-read
  from a systemd drop-in that uninstall also removes: a recorded `plaintext` or
  `external-tls` publication implies `NEXA_ALLOW_INSECURE_HTTP=1`.
- A recorded HTTPS node whose certificate is *also* gone is refused with an
  error naming both remedies, instead of being silently downgraded.
- Vhost inspection survives only as the one-time migration for a pre-record node,
  and as the fallback when no nexa binary can read the record and the vhost is
  still there to be left alone.
- `uninstall.sh` states the retention explicitly, and the record is snapshotted
  by self-update so a rolled-back update restores it alongside the vhost it
  describes.

Proven by: `scripts/test-node-lifecycle.sh` scenario 4, which now uninstalls and
reinstalls **with no publishing flags at all** and asserts the all-interfaces
listener and the cleartext decision both came back; two container tests in
`scripts/lifecycle_contract_test.go` covering the recorded-HTTPS reinstall and
the refusal when the certificate is gone; and `test-vm-lifecycle.sh reinstall`,
which is the live-hardware half — uninstall, flagless reinstall, then a public
`curl` with no `-k` on the original hostname. That last one **passed on real
hardware** on 2026-07-23; see 10.5.

### 10.4 What this says about the acceptance strategy

The container node proved none of these. Seven defects surfaced within a few
hours of real-hardware testing, three of which (1, 5, 6) would each have made the
product unusable for its first customer. OPS-001 remains the highest-value open
gate: until the lifecycle runs on real or virtualised hosts in CI, this class of
defect is found by users.

Re-run the full sequence on a clean box now that all seven fixes have landed. The
procedure is `docs/live-test-plan.md`; the session handoff is
`docs/live-test-handoff.md`.

### 10.5 Second live-node qualification, 2026-07-23

A second Linode Ubuntu 24.04.4 AMD64 box (2 GiB / 1 vCPU), hostname
`panel.panjnadvetclinic.com`, with all seven fixes already in the bundle.

**The whole matrix passed**, `test-vm-lifecycle.sh all` at commit `2c701b4`:

| # | Scenario | Result |
| --- | --- | --- |
| 1 | fresh TLS install | pass — real certificate, `doctor` reports 0 blockers |
| 2 | uninstall then flagless reinstall | pass — defect 7 proven on hardware |
| 3 | N-1 -> N update | pass — 0.3.0 -> 0.4.0 |
| 4 | offline rollback, services stopped | pass — after a harness fix, below |
| 5 | injected update failure | pass — node stayed on 0.3.0 |
| 6 | reboot | pass — 0 failed units, public HTTPS immediately |

This is the first install to go start to finish on unmodified code: the previous
session landed six fixes mid-run and a seventh after it.

Scenario 2 is the one that mattered. The retain-data uninstall kept
`/etc/nexa-panel/publishing.json`, the flagless reinstall republished from it and
re-deployed the retained certificate without issuing anything, and an ordinary
public `curl` returned 200. `nexa publishing show` reports `Source: install`.

Two defects found, neither by the matrix itself:

- **Defect 8** (10.1) — found by driving the panel UI, not the scenarios. No
  lifecycle scenario installs an application, which is why six scenarios could
  pass on a node where the Applications page could not install anything at all.
  The gap is worth closing: a scenario that installs one catalog application
  would have caught it.
- **A harness defect**, not a product one: `scenario_offline_rollback` compared
  the transaction journal's bare `previousVersion` ("0.3.0") against
  `nexa version`'s stamped line ("0.3.0 (commit …, built …)"). That can never
  hold, so the scenario failed on a node that had rolled back perfectly, and
  aborted before the assertions that mattered ever ran. Guarded now by
  `TestVersionAssertionsCompareLikeWithLike`.

Also worth fixing: `docs/live-test-plan.md` tells the operator to run
`test-vm-lifecycle.sh` from an unpacked release tree at `/opt/nexa-src`, but
`build-linux-release.sh` does not ship the harness in the bundle — it stages only
the files `install.sh` reads. The harness has to be copied to the box separately.

### 10.6 Defect 9: an update that succeeds must not report failure

Found at the end of the release session, on the first release the node ever
self-updated from. The update worked — `swapped=true activated=true
packagingSynced=true`, `nexa version` reporting the new release, public HTTPS
200 — and `nexa self-update` still exited non-zero with
`apply update: call self-update agent: Post "http://unix/v1/self-update/apply": EOF`.

The cause is structural rather than accidental. `waitForActivation` runs *inside
nexa-agent*, and the activation helper's job is to restart nexa-agent. The
process that owes the operator an answer is killed by the act it is reporting
on, so the response can never arrive. Nothing was wrong with the update; the only
thing broken was the report of it.

This is the same family as defects 4, 6 and 7: the stated outcome did not match
what happened. It is arguably the worst of them, because the two obvious
reactions to "your update failed" — retry it, or start recovering the node — are
both wrong and both destructive on a node that is already correctly updated.

The fix makes the durable journal, not the HTTP response, the thing that reports
the outcome:

- The agent gains `GET /v1/self-update/transaction`, a read-only projection of
  the journal: transaction id, phase, whether that phase is terminal, and the
  committed `Result`.
- `UnixClient.Apply` reads that journal before applying and, when the response is
  severed mid-exchange, polls it until a transaction with a *different* id
  reaches a terminal phase, then reports what that transaction committed.
  Requiring a new id is what stops a stale success from being reported as this
  apply's; a severed response with no new committed transaction is still a
  failure, as is one from a node whose journal cannot be read at all.
- Only a connection that dies mid-exchange resumes. A request the agent answered
  with an error, and one that never reached it, stay ordinary failures.

The second half was in the same journal: the agent exited 1 on `SIGTERM` with
`context deadline exceeded`, so systemd marked the unit failed during every
update. Its 5 s shutdown drain cannot complete while an apply is in flight —
that request is waiting on the restart doing the draining. A stop signal is an
instruction, not a failure, so the drain expiring now forces the remaining
connections closed and exits 0.

Both halves landed with the assertion first, each confirmed red against the exact
live symptom: `TestApplyReportsTheCommittedOutcomeWhenActivationRestartsTheAgent`
reproduced the `EOF`, and `TestServeExitsCleanlyWhenAStopSignalArrivesDuringAnApply`
reproduced the `context deadline exceeded`.

**Not yet verified on hardware.** Proving it needs a published release the node
can update to, and the box's installed agent predates the journal route — the
same bootstrap trap as the extractor fix. A node updating *from* an agent without
that route degrades to the old reported failure; the resume takes effect from the
first update that starts on a build carrying it.

### 10.7 Defect 10: no vendor-repository database engine could be installed

Found by installing MariaDB 13.0 from the Applications page, on a node already
carrying the defect-8 fix. The job failed in six seconds with:

```
configure the MariaDB 13.0 repository: exit status 1:
gpg: Fatal: can't create directory '/root/.gnupg': Read-only file system
signing key fingerprint mismatch: downloaded none, pinned 177F4010FE56CA…
```

Two lines, and the second is a lie caused by the first. `addRepoScript` reads the
downloaded key with `gpg --show-keys`, but `nexa-agent.service` sets
`ProtectHome=true`, so gpg could not create `$HOME/.gnupg` and exited before
reading anything. `got_fpr` was therefore empty, and the pin check reported it as
`downloaded none` — pointing the operator at the vendor's signing key, which was
never involved. Every MySQL and MariaDB series that needs a vendor repository was
uninstallable on every node.

This is defect 8's shape exactly: a hardening directive on the unit withholding
something a child process needs, invisible because nothing exercises that child
under the unit's sandbox. The fix is the narrow one — the script exports a
private `GNUPGHOME` inside the work directory it already creates and cleans up,
rather than widening `ReadWritePaths` or relaxing `ProtectHome`.

The deeper gap is that `addRepoScript` had never been executed by any test. Every
existing assertion about it stops at the fake runner and inspects the command
*string*. `addrepo_script_test.go` now runs the real script against a stub `curl`
and a checked-in throwaway vendor key, under a `HOME` that cannot be created —
which reproduced the live failure verbatim, `downloaded none` and all — and
covers both the success path and the pin refusal.

**Still unproven:** the `ppa:ondrej/php` and PGDG repository paths also shell out
to tools that use gpg. PostgreSQL 18 did install from the panel on the test box,
so PGDG is fine in practice; `add-apt-repository` under the agent's sandbox has
not been exercised since defect 8 was fixed and should be, by installing a PHP
version the node does not already carry.

*(That last paragraph was written at 16:20 and was obsolete by 16:45. Installing
PHP 8.5 is exactly what found defect 11.)*

### 10.8 Defect 11: PHP was uninstallable for the same reason MariaDB was

Found minutes after defect 10 was verified fixed on hardware, by doing the one
thing 10.7 named as unproven. Installing PHP 8.5 from the Applications page
failed in nine seconds with a Python traceback ending:

```
File "/usr/lib/python3/dist-packages/launchpadlib/launchpad.py", line 789, in _get_paths
    os.makedirs(launchpadlib_dir, 0o700)
OSError: [Errno 30] Read-only file system: '/root/.launchpadlib'
```

`add-apt-repository ppa:ondrej/php` resolves the PPA through launchpadlib, which
caches into `$HOME/.launchpadlib`. `ProtectHome=true` makes that `/root` and
read-only, so it aborted before reaching the network. Same mechanism as defect
10, different tool, and equally total: no PHP version could be installed from the
panel on any node.

Fix: repository tooling is handed a writable `HOME` at `/var/lib/nexa-panel-apt`
— beside the self-update work root, root-owned, on a path already in the unit's
`ReadWritePaths`, so the sandbox is not widened.

The directory is created and then `chmod`ed explicitly rather than trusting
`MkdirAll`'s mode. The agent runs with `UMask=0177`, which masks `MkdirAll(0700)`
down to `0600` — a directory with no execute bit that nothing can enter.
`TestToolHomeIsUsableUnderTheAgentUmask` sets that umask around the call and
asserts the mode; removing the `chmod` fails it with `mode = -rw-------`.

**The pattern is now three deep — defects 8, 10 and 11 are all the same defect.**
A hardening directive on `nexa-agent.service` withholds something a child process
needs, and nothing catches it because no test runs those children under the
unit's sandbox. `CAP_SETUID` for apt's `seteuid`, `$HOME` for gpg, `$HOME` for
launchpadlib. The next one will be found the same way — by a human clicking a
button — unless a lifecycle scenario starts installing from each repository
class under the real unit. That is now the single highest-value piece of test
work outstanding; see the standing handoff.
