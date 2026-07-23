# Nexa Panel v1 Release Plan

> Status: **release blocked**
>
> Updated: 2026-07-22
>
> Target: the first production release for Ubuntu 24.04 LTS on AMD64 and ARM64

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
| Control plane | Go modular monolith, SQLite/WAL state, embedded migrations, encrypted secrets, Unix-socket HTTP | Contract generation, migration rollback policy, production topology tests |
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
  `nexa-node` currently returns 404 because the control plane cannot recognize
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

- Keep the control plane/agent split, but make privileged interfaces narrow and
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
