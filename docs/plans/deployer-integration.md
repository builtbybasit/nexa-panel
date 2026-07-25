# Deployer integration — implementation plan

Status: proposed. Scope: exactly four phases (per-site SSH access, GitHub access,
deployer-aware site layout, server prerequisites). Anything beyond these four
phases — deploy history UI, webhooks, rollback-to-release, `deploy.php` authoring
— is explicitly out of scope for this plan.

Target branch base: `release/v1-hardening` (or `main` after it merges).

---

## 0. Naming, module identity, and cross-cutting decisions

### 0.1 One new control-panel module and one new operator

| Layer | Package | ID |
|---|---|---|
| Control panel | `internal/modules/deploy` | module id `deploy` |
| Privileged node operator | `internal/platform/operators/deploy` | agent routes under `/v1/deploy/…` |
| Frontend | `web/src/modules/sites` (sub-pages) + new `web/src/modules/deploy` for the shared views | route `/sites/:siteId/deployment` |

Rationale: every user-facing surface is per-site, and the site detail page already
owns the FeatureTile grid (`web/src/modules/sites/views/SiteDetailView.vue:127-143`).
The Go module is separate from `internal/modules/sites` because it owns its own
tables, its own job kinds, and its own operator; `sites` stays the assembler of
`siteoperator.Site` (`internal/modules/sites/definition.go:24`).

### 0.2 Permissions (SECURITY DECISION)

Add to `internal/platform/authorization/authorization.go` const block (after line 40):

```go
DeployRead  Permission = "deploy.read"
DeployWrite Permission = "deploy.write"
```

Grants in `New()` (`authorization.go:50-65`):

- `viewer`: `deploy.read`
- `developer`: `deploy.read`, `deploy.write`
- `operator`: both
- `admin`: both

`deploy.write` **is MFA-sensitive**: extend `sensitivePermission`
(`authorization.go:95-97`) with `|| permission == DeployWrite`. It installs SSH
authorized keys, changes a login shell, and writes a sudoers drop-in — every one
of those is a lateral-movement primitive.

Mirror in `web/src/modules/identity/permissions.ts`: union at `:3-31`, plus
`readOnlyPermissions` / `developerPermissions` / `operatorPermissions` arrays.
Extend the matrix in `internal/platform/authorization/authorization_test.go:24-56`.

### 0.3 Secrets policy (SECURITY DECISION)

- **No private key ever reaches the control panel.** The deploy key is generated
  **on the node** by the operator (`ssh-keygen -t ed25519 -N ''`), written to
  `{root}/.ssh/id_ed25519` mode `0600` owned by `nexa_<slug>`, and the operator
  returns **only** the public key + fingerprint. The `site_deploy_keys` table has
  **no ciphertext column**. This deliberately rejects the "encrypted private key
  column" template: a job payload and a `control.db` backup then cannot leak a
  key that grants repository access.
- **No secret in a job payload.** Job request JSON is persisted
  (`internal/platform/jobs/repository.go:107,126`). The `shared/.env` editor
  therefore writes synchronously through the operator (the SFTP precedent,
  `internal/modules/sftp/sftp.go:1-6`), not through a job.
- **`shared/.env` content is never audited or logged.** Audit metadata carries
  byte length and SHA-256 digest only.

### 0.4 Agent sandbox

Everything this plan writes lands in `/etc/ssh/sshd_config.d`,
`/etc/nexa-panel/generated/deploy`, `/etc/sudoers.d`, and `/srv/nexa/sites` — all
already inside `ReadWritePaths` (`packaging/systemd/nexa-agent.service:42`).
**No change to that line and therefore no change to
`packaging/security_contract_test.go:130` is required.** If any work item finds
itself wanting a new root, stop and revisit — that test asserts the word set
exactly and demands exactly one `ReadWritePaths=` line.

### 0.5 Migration numbering

Highest existing prefix is `20260722000002`. This plan allocates, in order:

- `20260722000003_site_ssh_access` (Phase 1)
- `20260722000004_site_deploy_keys` (Phase 2)
- `20260722000005_site_deployment_mode` (Phase 3)

All as `.tx.up.sql` / `.tx.down.sql` pairs at repo root in `migrations/`,
multi-statement files separated by `--bun:split`. **Do not** add entries to
`legacyLedger` (`internal/platform/persistence/migrations.go:65-91`) — it stops
at `20260721000025` and a post-cutover entry breaks `preseedLegacy`.

### 0.6 Domain language

`CONTEXT.md` is a mandatory glossary that every prior feature extended. Add,
under a new "Deployments" grouping: **Deployer Mode**, **Release**, **Current
Release**, **Shared Path**, **Deploy Key**, **SSH Access**, **Prepared Node**.
Each entry gets the `_Avoid_:` line the file's existing entries carry (e.g.
Release — _Avoid_: "build", "version", "artifact").

Also move the `PLAN.md:167` bullet ("Git deployments and zero-downtime release
directories") out of `### Later` into a new milestone under §20, and extend §15
(HTTP interface) and §17 (repository layout) with the new package paths.

### 0.7 Gates every phase must pass

`make check` = `fmt-check mod-check vet go-staticcheck go-deadcode go-vulncheck
test scripts-check web-test web-typecheck web-build web-deadcode web-audit
openapi-lint` (`Makefile:83-84`). Specifically:

- `go-staticcheck` runs twice, plain and `-tags embed`, with `-checks=all,-ST1000,-ST1005`.
- `mod-check` is `go mod tidy -diff`. **This plan adds no Go dependency** — all
  key generation and fingerprinting shells out to `ssh-keygen` on the node
  (`openssh-server` is already a prerequisite, `scripts/install.sh:180`, and
  `ssh-keygen -A` already runs at `install.sh:320`). Do not reach for
  `golang.org/x/crypto/ssh`; it is not in `go.mod` today and adding it drags
  `go-vulncheck` and `mod-check` into every phase.
- `web-deadcode` is knip with **no config file**, including `--production` mode —
  unused exports and files fail the build. Do not add barrel exports nothing imports.
- OpenAPI: `openapi-lint` only lints what exists, and several shipped modules
  (sftp, firewall, services) have no spec. Documenting `deploy` is **optional**;
  this plan does it in Phase 4 as a single batch so it does not block earlier phases.

---

## Phase 1 — Per-site SSH access

**Goal:** a site's own Unix account (`nexa_<slug>`) can log in over SSH with a
real shell, key-only, with a panel-managed authorized-keys list, without
touching the SFTP module's code or its chroot invariants.

### 1.0 The collision, and the decision that resolves it (SECURITY DECISION)

Facts that constrain this phase:

1. The account is created once, at site activation, with
   `--shell /usr/sbin/nologin` (`internal/platform/operators/sites/host_system.go:61`),
   and nothing in the repo ever runs `usermod`/`chsh`. `PrepareSite` only
   `useradd`s when lookup returns `UnknownUserError` (`host_system.go:56-63`), so
   changing the `useradd` flags does **not** retrofit existing sites.
2. The site root is `root:root 0755` on purpose so it can be an sshd
   `ChrootDirectory` (`host_system.go:86-93`, `operators/sftp/sftp.go:9-10`).
   Making it user-writable breaks the SFTP jail. It must stay as-is.
3. The SFTP drop-in `/etc/ssh/sshd_config.d/nexa-site-<slug>.conf` renders
   `Match User nexa_<slug>` with `ChrootDirectory` and
   `ForceCommand internal-sftp -d /public` unconditionally
   (`operators/sftp/sftp.go:80,88-101`). sshd applies **all** matching `Match`
   blocks but the **first** setting of each keyword wins, and `Include` globs are
   read in sorted order.

**Decision:**

- The SSH drop-in is a **separate file**, `/etc/ssh/sshd_config.d/nexa-access-<slug>.conf`.
  `nexa-access-…` sorts before `nexa-site-…`, so when both exist the SSH block's
  `ForceCommand none` / `ChrootDirectory none` win. That is precisely why we must
  never allow both to be enabled at once.
- **SSH access and SFTP access are mutually exclusive per site, enforced in the
  control panel, with the SFTP module unmodified except for one additive
  read-only accessor.** Enabling SSH while SFTP is on returns HTTP 409
  `sftp_access_enabled` with a message telling the operator to disable SFTP
  first; the SFTP module's own enable path is unchanged, so the reverse race is
  closed by a second check inside the SSH operator (see 1.4) that refuses to
  install the drop-in when `nexa-site-<slug>.conf` exists on disk.
- **`authorized_keys` lives outside the home**, at
  `/etc/nexa-panel/generated/ssh/<unixuser>/authorized_keys`, `0644 root:root`
  (parent dir `0755 root:root`), referenced by an explicit `AuthorizedKeysFile`
  in the Match block. Reason: the site root is root-owned and `StrictModes`
  refuses a home whose ownership it distrusts; a root-owned key file also means a
  compromised site process cannot append its own key. This is a deliberate
  deviation from the literal "managed `~/.ssh/authorized_keys`" wording, and the
  UI copy must say where the file lives.
- **A writable `~/.ssh` is still created** — `{root}/.ssh`, `0700`,
  `nexa_<slug>:nexa_<slug>` — because Phase 2 needs `known_hosts` and the deploy
  key there and git needs a `HOME`. Root can create a child of the root-owned
  site root; the site account cannot create or rename entries in `{root}` itself,
  so the chroot invariant is untouched. Note the residual: an SFTP session for
  the same site could read `{root}/.ssh` — but it is the same principal, so this
  grants nothing new. Do not put anything there that the site user should not see.
- **Shell flip**: enable runs `usermod -s /bin/bash nexa_<slug>`; disable runs
  `usermod -s /usr/sbin/nologin nexa_<slug>`. The shell is a new privileged
  `NodeSystem` call, not a change to site provisioning.
- **Key-only.** The Match block sets `PasswordAuthentication no`,
  `AuthenticationMethods publickey`, `PubkeyAuthentication yes`. The panel never
  issues a password for SSH, so the SFTP module's `passwd -l` on disable
  (`operators/sftp/system.go:88-93`) cannot lock SSH out.

### 1.1 Work items

---

**P1-A · `PARALLEL-GROUP P1-A` — permissions**

Edit:
- `internal/platform/authorization/authorization.go` — const block, grant maps, `sensitivePermission`.
- `internal/platform/authorization/authorization_test.go` — extend `permissions` slice (`:24-30`) and expected matrix (`:33-56`).
- `web/src/modules/identity/permissions.ts` — union `:3-31`, role arrays `:33-85`.

Nothing else in P1 may touch these three files.

---

**P1-B · `PARALLEL-GROUP P1-B` — privileged SSH-access operator**

Create `internal/platform/operators/deploy/ssh.go`:

```go
const (
	SSHDDropInDir     = "/etc/ssh/sshd_config.d"
	AuthorizedKeysDir = "/etc/nexa-panel/generated/ssh"
	siteRootBase      = "/srv/nexa/sites"
	loginShell        = "/bin/bash"
	nologinShell      = "/usr/sbin/nologin"
)

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// AuthorizedKey is one installed public key. Comment is the operator-visible
// label; it is re-emitted verbatim as the trailing comment field.
type AuthorizedKey struct {
	Algorithm string `json:"algorithm"` // "ssh-ed25519" | "ssh-rsa" | "ecdsa-sha2-nistp256" …
	Blob      string `json:"blob"`      // base64 body, no whitespace
	Comment   string `json:"comment"`
}

type SSHAccessRequest struct {
	Slug     string          `json:"slug"`
	UnixUser string          `json:"unixUser"`
	RootPath string          `json:"rootPath"`
	Enabled  bool            `json:"enabled"`
	Keys     []AuthorizedKey `json:"keys"`
}

type SSHAccessObservation struct {
	Slug               string `json:"slug"`
	Enabled            bool   `json:"enabled"`
	Username           string `json:"username"`
	Shell              string `json:"shell"`
	DropInPath         string `json:"dropInPath"`
	AuthorizedKeysPath string `json:"authorizedKeysPath"`
	KeyCount           int    `json:"keyCount"`
}

func (r SSHAccessRequest) validate() error
func (r SSHAccessRequest) dropInPath() string          // /etc/ssh/sshd_config.d/nexa-access-<slug>.conf
func (r SSHAccessRequest) authorizedKeysPath() string  // /etc/nexa-panel/generated/ssh/<unixUser>/authorized_keys
func renderSSHDropIn(r SSHAccessRequest) string
func renderAuthorizedKeys(keys []AuthorizedKey) string
```

`validate()` copies the SFTP trust boundary verbatim
(`operators/sftp/sftp.go:63-77`): slug matches `slugPattern`,
`r.UnixUser == "nexa_"+strings.ReplaceAll(r.Slug,"-","_")`,
`filepath.Clean(r.RootPath) == filepath.Join(siteRootBase, r.Slug)`. Plus, per
key: `Algorithm` against a fixed allowlist (`ssh-ed25519`, `ssh-rsa`,
`ecdsa-sha2-nistp256/384/521`, `sk-ssh-ed25519@openssh.com`), `Blob` matching
`^[A-Za-z0-9+/]+={0,3}$` and ≤ 16 KiB, `Comment` ≤ 128 chars with no
`\x00\r\n` and no leading `-`. `renderAuthorizedKeys` emits exactly
`<algorithm> <blob> <comment>\n` per key with no options field — **never**
interpolate a caller string before the algorithm token, or an attacker sets
`command=`/`environment=` options.

`renderSSHDropIn` (pure `strings.Builder`, no template, mirroring
`operators/sftp/sftp.go:88-101`):

```
# Managed by Nexa Panel. Per-site SSH access — do not edit.
Match User <UnixUser>
    ChrootDirectory none
    ForceCommand none
    AuthorizedKeysFile <authorizedKeysPath>
    PubkeyAuthentication yes
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    AuthenticationMethods publickey
    AllowTcpForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTunnel no
    PermitTTY yes
```

Create `internal/platform/operators/deploy/ssh_host.go`:

```go
type SSHNodeSystem interface {
	SFTPDropInExists(ctx context.Context, slug string) (bool, error)
	EnsureHomeSSHDir(ctx context.Context, rootPath, unixUser string) error
	WriteAuthorizedKeys(ctx context.Context, path, content string) error
	RemoveAuthorizedKeys(ctx context.Context, path string) error
	WriteDropIn(ctx context.Context, path, content string) error
	RemoveDropIn(ctx context.Context, path string) error
	SetShell(ctx context.Context, user, shell string) error
	ValidateSSHD(ctx context.Context) error
	ReloadSSHD(ctx context.Context) error
}

type SSHHostOperator struct{ system SSHNodeSystem }
func NewSSHHostOperator(system SSHNodeSystem) (*SSHHostOperator, error)
func (o *SSHHostOperator) ApplySSHAccess(ctx context.Context, request SSHAccessRequest) (SSHAccessObservation, error)

var ErrSFTPJailPresent = errors.New("per-site SFTP is enabled for this site")
```

Enable order (copy the rollback discipline of `operators/sftp/host.go:47-73`):
`validate` → `SFTPDropInExists` (abort with `ErrSFTPJailPresent`) →
`EnsureHomeSSHDir` → `WriteAuthorizedKeys` → `WriteDropIn` → `ValidateSSHD` →
`ReloadSSHD` → `SetShell(loginShell)`. On a failed validate/reload, remove the
drop-in **and** the authorized-keys file under `context.WithoutCancel(ctx)` and
re-run `sshd -t` before returning.

Disable order: `SetShell(nologinShell)` → `RemoveDropIn` → `RemoveAuthorizedKeys`
→ `ValidateSSHD` → `ReloadSSHD`. The `.ssh` directory and its contents are left
in place (Phase 2 owns them); disabling SSH must not destroy a deploy key.

Create `internal/platform/operators/deploy/ssh_system.go` — the real
`SSHNodeSystem`, modelled on `operators/sftp/system.go`:
- `atomicWrite(path, content, mode)` helper (sibling temp file, chmod on fd, `Sync`, `Rename`) — copy `operators/sftp/support.go:33-57`.
- `EnsureHomeSSHDir`: `os.OpenRoot(rootPath)`, `Lstat(".ssh")`, reject symlink/non-dir, `MkdirAll` if absent, then fd-relative `Chmod(0o700)` + `Chown(uid, gid)` with uid/gid from `user.Lookup(unixUser)` — mirror `operators/sites/host_system.go:145-175`.
- `SetShell`: `usermod -s <shell> <user>`.
- `ValidateSSHD` / `ReloadSSHD`: `sshd -t`, `systemctl reload ssh.service`.
- `SFTPDropInExists`: `os.Lstat(filepath.Join(SSHDDropInDir, "nexa-site-"+slug+".conf"))`.

Create `internal/platform/operators/deploy/support.go`:

```go
type UnixClient struct{ client *agentclient.Client }
func NewUnixClient(socketPath, tokenPath string) *UnixClient   // agentclient.New(..., "deploy", "node agent rejected the deploy operation", 5*time.Minute)
func (c *UnixClient) ApplySSHAccess(ctx context.Context, request SSHAccessRequest) (SSHAccessObservation, error)
```

Timeout note: 30 s (the SFTP figure) is fine for Phase 1, but Phase 2's
`git ls-remote` and Phase 4's apt run share this client. Set **5 minutes** now
for SSH/GitHub and give the Phase-4 prepare endpoint its own 30-minute client
(the PHP operator precedent, `operators/php/client.go:20-22`).

Also create `internal/platform/operators/deploy/ssh_test.go`: rendering golden
tests, `validate` rejection of a forged unix user / root path (mirroring
`operators/sftp/sftp_test.go:125-142`), call-order assertions with a fake
`SSHNodeSystem`, and an option-injection attempt in `Comment`/`Blob`.

---

**P1-C · `PARALLEL-GROUP P1-C` — agent wiring**

Create `internal/platform/agent/deploy_handlers.go`:

```go
func WithDeployOperator(operator deployoperator.Operator) Option
func (s *Server) deploySSHApplyHTTP(w http.ResponseWriter, r *http.Request)
```

Decode with the package `decodeJSON` (`internal/platform/agent/http_support.go:6-10`),
map `ErrSFTPJailPresent` to `409 sftp_jail_present` and everything else to
`409 deploy_operation_failed`, `400 invalid_request` on a bad body. Follow the
typed-error precedent if it grows: `internal/platform/agent/schedule_handlers.go:22-29`.

Edit `internal/platform/agent/server.go`:
- import `deployoperator "…/internal/platform/operators/deploy"` near line 21
- field `deploy deployoperator.Operator` near line 52
- in `Serve`, nil-guarded route block near line 181:
  `mux.HandleFunc("POST /v1/deploy/ssh/apply", s.deploySSHApplyHTTP)`

Edit `cmd/nexa/agent.go`: construct `deployOperator, err := deployoperator.NewHostOperator(nil)`
near line 99 and append `agent.WithDeployOperator(deployOperator)` to the
`agent.New(...)` call at line 117.

> Sequencing: P1-C depends on P1-B's exported types existing. Land P1-B first, or
> have the P1-C agent stub the interface against an agreed signature.

---

**P1-D · `PARALLEL-GROUP P1-D` — migration**

Create `migrations/20260722000003_site_ssh_access.tx.up.sql`:

```sql
CREATE TABLE site_ssh_access (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	enabled BOOLEAN NOT NULL DEFAULT 0,
	username TEXT NOT NULL,
	shell TEXT NOT NULL DEFAULT '/usr/sbin/nologin',
	enabled_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL
);
--bun:split
CREATE TABLE site_ssh_keys (
	id TEXT PRIMARY KEY,
	site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	label TEXT NOT NULL,
	algorithm TEXT NOT NULL,
	public_key TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	comment TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	UNIQUE(site_id, fingerprint)
);
--bun:split
CREATE INDEX site_ssh_keys_site_idx ON site_ssh_keys (site_id, created_at);
```

Down: `DROP TABLE IF EXISTS site_ssh_keys;` `--bun:split` `DROP TABLE IF EXISTS site_ssh_access;`

Note the `REFERENCES sites(id) ON DELETE CASCADE` — do **not** copy
`sftp_access`'s missing FK (`migrations/20260721000019_sftp.tx.up.sql:2`);
`foreign_keys(1)` is on (`internal/platform/persistence/sqlite.go:34`) and
`sites` `deleteJob` relies on cascade (`internal/modules/sites/teardown.go:131-160`).

---

**P1-E · `PARALLEL-GROUP P1-E` — control-panel module (depends on P1-B, P1-D)**

Create `internal/modules/deploy/deploy.go`:

```go
// Package deploy drives the privileged deploy operator. SSH-access and
// deploy-key operations run synchronously on the request goroutine — never as a
// durable job — because a job payload is persisted and neither an authorized-key
// install nor a key generation may leave a record outside the node.
type SiteCatalog interface{ Get(ctx context.Context, id string) (sites.Site, error) }
type AccessPolicy interface{ SiteAccessible(ctx context.Context, user identity.User, siteID string) (bool, error) }
type SFTPState  interface{ AccessEnabled(ctx context.Context, siteID string) (bool, error) }

type Module struct { /* unexported */ }

func New(_ context.Context, database *bun.DB, operator deployoperator.Operator,
	queue *jobs.Module, catalog SiteCatalog, access AccessPolicy, sftp SFTPState) (*Module, error)

func (m *Module) Descriptor() module.Descriptor  // ID "deploy", deps {"identity","sites","jobs"}
func (m *Module) Register(registry module.Registry) error
```

Create `internal/modules/deploy/models.go` — `sshAccessModel`,
`sshKeyModel` (bun structs per `internal/modules/sftp/sftp.go:41-48`), exported
`SSHAccess` / `SSHKey` API types, `func randomKeyID() string { return "sshkey_" + secureid.Hex(12) }`.

Create `internal/modules/deploy/ssh_http.go` — routes registered in
`registerHTTP` through `registry.HandleAuthorized`:

| Pattern | Permission |
|---|---|
| `GET /api/v1/sites/{id}/ssh` | `deploy.read` |
| `POST /api/v1/sites/{id}/ssh/enable` | `deploy.write` |
| `POST /api/v1/sites/{id}/ssh/disable` | `deploy.write` |
| `POST /api/v1/sites/{id}/ssh/keys` | `deploy.write` |
| `DELETE /api/v1/sites/{id}/ssh/keys/{keyId}` | `deploy.write` |
| `POST /api/v1/sites/{id}/ssh/keys/generate` | `deploy.write` |

Handler rules:
- `resolveSite` copied from `internal/modules/sftp/support.go:43-64` — an
  inaccessible site returns the **same 404** as a missing site so scoped roles
  cannot probe existence.
- Every mutation writes `m.jobs.Audit().RecordSensitive` **before** calling the
  operator, mapping `audit.ErrUnauditable` to `503 audit_unavailable`
  (`internal/modules/firewall/http.go:58-61`). Actions:
  `deploy.ssh_enabled`, `deploy.ssh_disabled`, `deploy.ssh_key_added`,
  `deploy.ssh_key_removed`, `deploy.ssh_key_generated`. Subject `"site:"+siteID`.
  Metadata carries `fingerprint`, `label`, `algorithm` — **never the key blob**
  and never a private key.
- Enable refuses with `409 sftp_access_enabled` when `m.sftp.AccessEnabled` is true.
- Key parsing lives in `internal/modules/deploy/keys.go`:
  `func parseAuthorizedKey(line string) (algorithm, blob, comment string, err error)`
  — split on whitespace into at most 3 fields, reject a leading `-` or any
  `=`-bearing options field, validate base64, cap at 16 KiB. Fingerprint
  (`SHA256:<base64>`) is computed as `base64.RawStdEncoding(sha256(rawBlob))`
  with `crypto/sha256` + `encoding/base64` — stdlib only, no new dependency.
- `POST .../keys/generate` is the "optional in-panel ed25519 generation shown
  once": it calls a new synchronous operator method
  `GenerateUserKey(ctx, SSHAccessRequest) (GeneratedKey{PublicKey, PrivateKey, Fingerprint}, error)`
  which runs `ssh-keygen -t ed25519 -N '' -C "<slug>@nexa" -f <tmp>` in the
  agent's `PrivateTmp`, reads both halves, unlinks them, and returns them in the
  HTTP response. The **private half is returned exactly once in the response
  body and never persisted** — same contract as the SFTP password
  (`internal/modules/sftp/handlers.go:76-78`), and the frontend keeps it in a
  `ref` only. The public half is inserted into `site_ssh_keys` and installed.
- The DB write and the operator call are ordered DB-first-in-tx, operator-second,
  with a rollback of the tx if the operator fails — copy the upsert idiom at
  `internal/modules/sftp/handlers.go:101-114`.

Create `internal/modules/deploy/deploy_audit_test.go` mirroring
`internal/modules/firewall/firewall_audit_test.go:30-58`.

Edit `internal/modules/sftp/sftp.go` — add **one** additive exported method
(the only SFTP change in this plan):

```go
// AccessEnabled reports whether per-site SFTP is currently enabled. It exists
// so the deploy module can refuse to install a conflicting sshd Match block.
func (m *Module) AccessEnabled(ctx context.Context, siteID string) (bool, error)
```

Edit `cmd/nexa/api.go`: imports near lines 25 and 48; construct after
`sftpModule` (near line 199):

```go
deployModule, err := deploy.New(setupCtx, database,
	deployoperator.NewUnixClient(*agentSocket, *agentToken),
	jobsModule, sitesModule, identityModule, sftpModule)
```
wrapped as `fmt.Errorf("initialize deploy module: %w", err)`; append
`deployModule,` to the `modules` slice (`api.go:217-238`).

---

**P1-F · `PARALLEL-GROUP P1-F` — frontend**

Create:
- `web/src/modules/deploy/api.ts` — `apiRequest` wrapper with prefix `'Deploy request'`; `getSSHAccess`, `enableSSHAccess`, `disableSSHAccess`, `addSSHKey`, `removeSSHKey`, `generateSSHKey`. `encodeURIComponent` every id.
- `web/src/modules/deploy/api.test.ts` — assert exact fetch args in the style of `web/src/modules/sites/api.test.ts:99-104`, plus a `rejects.toThrow('<server message>')` case.
- `web/src/modules/deploy/components/SshAccessCard.vue` — enable/disable switch, key table (label, algorithm, fingerprint, added), "Add key" `FormField`+`AppTextarea` form with client-side prefix validation, "Generate a key" action rendering `CredentialReveal` for the one-time private half. Import `CopyField` as `@/shared/ui/CopyField.vue` (it is **not** in the barrel).
- `web/src/modules/deploy/views/SiteDeploymentView.vue` — the container page; `route.params.siteId`; `useQuery({queryKey:['sites'], queryFn: listSites, retry:false})` so it shares cache with the site detail page; site-scoped key `['deploy-ssh', () => siteId]`; pending/error/not-found triple; `PageHeader` with `eyebrow` and a "← Back to site" `RouterLink`.

Edit:
- `web/src/modules/sites/index.ts` — append
  `{ path: '/sites/:siteId/deployment', name: 'site-deployment', component: () => import('@/modules/deploy/views/SiteDeploymentView.vue'), meta: { moduleId: 'sites' } }`.
  **`meta.moduleId` is mandatory** (`web/src/modules/registry.test.ts:20`). Keeping
  the route inside the `sites` module avoids adding a sidebar entry for a
  per-site page.
- `web/src/modules/sites/views/SiteDetailView.vue` — add a tile in `tiles`
  (`:127-143`) before the Settings push: `if (identity.can('deploy.read')) items.push({ label: 'Deployment', icon: 'rocket', to: \`/sites/${id}/deployment\` })`.
- `web/src/shared/ui/icons.ts` — add `rocket` (lucide `Rocket`) to `iconMap` if a
  new glyph is wanted; otherwise reuse the existing `upload` or `terminal` and
  skip this edit. A typo silently renders `circle-question-mark`.

Beware `exactOptionalPropertyTypes`-style binding: optional props go through a
computed spread (`v-bind="progressExtras"`), never `:job-id="maybeUndefined"`.

### 1.2 Ordering and parallelism

```
P1-A ─┐
P1-D ─┤
P1-B ──→ P1-C ─┐
               ├──→ P1-E ──→ P1-F
P1-A, P1-D ────┘
```

P1-A, P1-B, P1-D, and P1-F's `SshAccessCard.vue`/`api.ts` scaffolding can all run
in parallel (disjoint files). P1-C needs P1-B's types. P1-E needs P1-B + P1-D.
P1-F's `SiteDetailView.vue` edit is the only frontend file that another agent
might also touch — serialize it.

### 1.3 Verification

Automated:
- `go test ./internal/platform/operators/deploy/...` — rendering goldens, forged
  identity rejection, enable/disable call order, rollback on `sshd -t` failure,
  option-injection rejection.
- `go test ./internal/modules/deploy/...` — audit fail-closed → 503, 404 for an
  inaccessible site, 409 when SFTP is enabled.
- `go test ./internal/platform/authorization/...`
- `bun run test` in `web/` for `api.test.ts`.
- `make check`.

Manual, on the test node (panel on port 8888):
1. Create a site, enable SSH access, add a local public key.
2. `ssh -i ~/.ssh/id_x nexa_<slug>@<node>` → a real shell in `/srv/nexa/sites/<slug>`.
3. `getent passwd nexa_<slug>` shows `/bin/bash`; after disable, `/usr/sbin/nologin` and SSH is refused.
4. `ssh -o PreferredAuthentications=password …` is refused (key-only).
5. With SFTP already enabled, the enable button returns the 409; disable SFTP, retry, succeeds.
6. `sshd -T -C user=nexa_<slug>` shows `forcecommand` absent and `chrootdirectory none`.
7. `nexa audit verify` still passes and the audit log shows `deploy.ssh_enabled`.

### 1.4 Known residual risks

- Site teardown still leaves the drop-in, the authorized-keys file, and the Unix
  account behind (`internal/modules/sites/teardown.go:105-168` only knows about
  domains and certificates). **Add a work item**: extend `deleteJob`'s hardcoded
  cleanup with a call into the deploy module to disable SSH access, or extend
  `dependentBlocker` to refuse deletion while SSH access is enabled. The blocker
  is the smaller change and matches existing behaviour; take it.
- If an operator hand-edits `/etc/ssh/sshd_config.d`, the mutual-exclusion
  invariant is only re-checked on the next enable.

---

## Phase 2 — GitHub access

**Goal:** each site gets a per-site ed25519 **deploy key** generated on the node,
its public half is copyable with a GitHub deep link, `known_hosts` is pre-seeded
with verified GitHub host keys, and a "Test connection" job proves it works.

Depends on Phase 1 (needs `{root}/.ssh` and the operator/module skeleton).

### 2.0 Decisions (SECURITY)

- The deploy key private half **never leaves the node** and is never stored in
  `control.db`. `site_deploy_keys` stores algorithm, public key, fingerprint,
  timestamps only. There is no reveal endpoint and no ciphertext column.
- `known_hosts` is **not** produced by `ssh-keyscan`. The GitHub host keys are
  embedded as a Go const in the operator and each is verified at render time by
  recomputing `SHA256:<base64(sha256(blob))>` and comparing against a hardcoded
  expected fingerprint. A mismatch is a hard error, not a warning.
- The connection test runs as the site user via `runuser -u nexa_<slug> --`
  (the precedent: `internal/platform/operators/schedules/host.go:199`). No
  sudoers rule; the agent is already root.

### 2.1 Work items

---

**P2-A · `PARALLEL-GROUP P2-A` — known-hosts material**

Create `internal/platform/operators/deploy/knownhosts.go`:

```go
type hostKey struct{ Algorithm, Blob, Fingerprint string }

// githubHostKeys are GitHub's published SSH host keys. Each entry's Fingerprint
// is checked against the recomputed SHA256 of Blob before anything is written,
// so a typo or a tampered constant fails closed instead of pinning a wrong key.
var githubHostKeys = []hostKey{ /* ssh-ed25519, ecdsa-sha2-nistp256, ssh-rsa */ }

func renderKnownHosts() (string, error)   // "github.com <alg> <blob>\n" per key
func verifyHostKey(k hostKey) error
```

> **OPEN QUESTION / implementer action:** the key blobs and their expected
> fingerprints must be transcribed from GitHub's published page
> (`https://docs.github.com/…/githubs-ssh-key-fingerprints`) **at implementation
> time and cross-checked from two network paths**. Known published fingerprints
> as of this writing — to be re-verified, not trusted from this document:
> ed25519 `SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU`,
> ecdsa `SHA256:p2QAMXNIC1TJYWeIOttrVc98/R1BUFWu3/LiyKgUfQM`,
> rsa `SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s`.
> A second open question: whether to also write `github.com` IP-range entries or
> rely on `CheckHostIP no` in the site's `~/.ssh/config`. Recommendation:
> write a minimal `~/.ssh/config` with `Host github.com` / `CheckHostIP no` /
> `IdentityFile ~/.ssh/id_ed25519` / `IdentitiesOnly yes`, which avoids the
> IP-pinning problem entirely.

Unit test: `renderKnownHosts` is deterministic and every entry self-verifies;
mutate a blob byte in a test copy and assert the error.

---

**P2-B · `PARALLEL-GROUP P2-B` — deploy-key operator surface**

Edit/extend `internal/platform/operators/deploy/ssh.go` and add
`internal/platform/operators/deploy/github.go`:

```go
type DeployKeyRequest struct {
	Slug     string `json:"slug"`
	UnixUser string `json:"unixUser"`
	RootPath string `json:"rootPath"`
	Rotate   bool   `json:"rotate"`
}

type DeployKeyObservation struct {
	Slug        string     `json:"slug"`
	Algorithm   string     `json:"algorithm"`
	PublicKey   string     `json:"publicKey"`
	Fingerprint string     `json:"fingerprint"`
	Path        string     `json:"path"`
	KnownHosts  bool       `json:"knownHosts"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type GitHubTestRequest struct {
	Slug, UnixUser, RootPath string
	Repository string `json:"repository"` // "git@github.com:owner/name.git"
}

type GitHubTestResult struct {
	AuthOK      bool   `json:"authOk"`
	Account     string `json:"account"`      // parsed from "Hi <account>!"
	LsRemoteOK  bool   `json:"lsRemoteOk"`
	RefCount    int    `json:"refCount"`
	OutputTail  string `json:"outputTail"`   // ≤ 64 KiB, secrets-free by construction
}
```

Operator methods on `SSHHostOperator`:

```go
func (o *SSHHostOperator) EnsureDeployKey(ctx context.Context, r DeployKeyRequest) (DeployKeyObservation, error)
func (o *SSHHostOperator) TestGitHub(ctx context.Context, r GitHubTestRequest) (GitHubTestResult, error)
```

`EnsureDeployKey`:
1. `validate()` (same identity boundary as Phase 1).
2. `EnsureHomeSSHDir`.
3. Write `known_hosts` (`0644`, owned `nexa_<slug>`) and `config` (`0600`) atomically.
4. If `{root}/.ssh/id_ed25519` is absent, or `Rotate` is set, run
   `ssh-keygen -t ed25519 -N "" -C "nexa-<slug>-deploy" -f {root}/.ssh/id_ed25519`
   (with `-f` pointing at a temp path in `PrivateTmp`, then atomic-rename into
   place with mode `0600` and `chown` on the fd) and unlink any stale `.pub`.
   Rotation must not leave a half-written key: write the new pair to
   `.ssh/id_ed25519.new`, fsync, then rename.
5. Read the `.pub`, parse and fingerprint it, return the observation. Never read
   or return the private half.

`TestGitHub` — validate `Repository` against
`^git@github\.com:[A-Za-z0-9._-]{1,64}/[A-Za-z0-9._-]{1,100}(\.git)?$` (an
`https://` form is rejected: this test exists to prove the *key* works), then run
both commands under `runuser -u <unixUser> --` with `HOME={root}` and
`GIT_TERMINAL_PROMPT=0`, each under a 30 s `context.WithTimeout` and an output
cap of 64 KiB (`operators/schedules/host.go:23-33` constants):
- `ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -T git@github.com` — exit
  code 1 with `Hi <account>! You've successfully authenticated` is **success**;
  exit 255 is failure.
- `git ls-remote --heads <repository>` — count the output lines.

Agent handlers (`internal/platform/agent/deploy_handlers.go`):
`POST /v1/deploy/key/ensure`, `POST /v1/deploy/github/test`. Register both in the
existing nil-guarded block in `internal/platform/agent/server.go`.

---

**P2-C · `PARALLEL-GROUP P2-C` — migration**

`migrations/20260722000004_site_deploy_keys.tx.up.sql`:

```sql
CREATE TABLE site_deploy_keys (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	algorithm TEXT NOT NULL,
	public_key TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	key_version INTEGER NOT NULL DEFAULT 1,
	repository TEXT NOT NULL DEFAULT '',
	last_tested_at TIMESTAMP,
	last_test_ok BOOLEAN,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
```

Down: `DROP TABLE IF EXISTS site_deploy_keys;`

Note deliberately: **no `private_key_ciphertext` column.**

---

**P2-D · `PARALLEL-GROUP P2-D` — control-panel endpoints + test job (depends on P2-B, P2-C)**

Edit `internal/modules/deploy/deploy.go` — register the job kind in the
constructor, **before** any submit
(`internal/platform/jobs/repository.go:72` rejects unregistered kinds):

```go
queue.RegisterHandler("deploy.github_test", m.githubTestJob)
```

Default `RecoveryFail` is correct — the test is read-only but the recovery
semantics should stay conservative and the run is cheap to repeat by hand.

Create `internal/modules/deploy/github_http.go`:

| Pattern | Permission |
|---|---|
| `GET /api/v1/sites/{id}/deploy-key` | `deploy.read` |
| `POST /api/v1/sites/{id}/deploy-key` | `deploy.write` (ensure; `{"rotate":true}` rotates) |
| `POST /api/v1/sites/{id}/deploy-key/test` | `deploy.write` → 202 `{"job": …}` |

`githubTestJob(ctx, raw, report)`:
`report(10, "Checking the GitHub host keys.")` →
`report(35, "Authenticating with GitHub over SSH.")` → operator `TestGitHub` →
`report(80, "Listing the repository's branches.")` →
`report(95, "GitHub access is verified.")` → return `GitHubTestResult` as the job
result. Progress must be monotonic and in `[0,99]`
(`internal/platform/jobs/worker.go:129-135`); 100 is reserved. The output tail is
surfaced in the job result JSON, which the view renders — this is what "streamed
to the job runner" means in practice: staged `report()` messages over SSE plus
the captured tail at the end.

Submit with `SubmitTitledWithOptions(..., SubmitOptions{ActorUserID: actor, SiteIDs: []string{siteID}})`
so `developer`-role users can see their own job (`internal/platform/jobs/access.go:64-87`).

Audit: `deploy.key_generated`, `deploy.key_rotated` (fail-closed, `RecordSensitive`),
`deploy.github_tested` (best-effort). Metadata: fingerprint + repository, never
key material.

---

**P2-E · `PARALLEL-GROUP P2-E` — frontend (depends on P2-D's shapes)**

Edit `web/src/modules/deploy/api.ts` + `api.test.ts`; create
`web/src/modules/deploy/components/DeployKeyCard.vue`:
- `CopyField` for the public key (`@/shared/ui/CopyField.vue`).
- Deep link button: `https://github.com/<owner>/<repo>/settings/keys/new` derived
  from the stored repository, falling back to `https://github.com/settings/keys`
  when no repository is set yet. Render with `rel="noopener noreferrer" target="_blank"`.
- Repository input with the same regex the operator enforces, validated in the
  `FormField`+`AppInput` per-field-error style of `SiteCreateView.vue:107-139`.
- "Test connection" button driving `useJobRunner`, with `JobProgress` +
  `JobFailureNotice` and a `<pre>` block for the returned output tail.
- Rotate action behind `AppConfirmDialog` with `typeToConfirm` set to the site's
  primary domain — rotation invalidates the key already registered on GitHub.

### 2.2 Verification

Automated: operator tests with a fake runner asserting the exact `runuser`/`ssh`/
`git` argv, the repository-regex rejections, the known-hosts self-verification,
and that no code path reads `id_ed25519`. Module test: 202 + job kind registered;
audit metadata contains no `BEGIN OPENSSH PRIVATE KEY`.

Manual: create a key, register it on a real GitHub repo as a deploy key, hit
"Test connection", watch the SSE progress, confirm `Hi <owner>/<repo>!` in the
tail. Then delete the key from GitHub and confirm the job fails with a readable
message. Confirm `ls -l /srv/nexa/sites/<slug>/.ssh` shows `id_ed25519` `0600`
owned by the site user and `known_hosts` `0644`.

---

## Phase 3 — Deployer-aware site layout

**Goal:** a site can be switched to `deployment_mode = "deployer"`, which creates
`releases/` and `shared/`, points nginx at `{root}/current/public`, seeds
`current` so `nginx -t` and the health probe pass before the first deploy, and
never fights Deployer's per-release ownership. Plus a `shared/.env` editor.

### 3.0 Decisions

This is the highest-risk phase because `Renderer.Render` must remain a **pure
function of `Site` + `Settings`** — `Apply` re-renders and demands byte-exact
equality (`internal/platform/operators/sites/activation.go:116`,
`artifacts.go:15-32`). Therefore:

1. **`deployment_mode` becomes a field on `siteoperator.Site`**, threaded through
   `internal/modules/sites/definition.go:24` like `Settings` already is. The
   renderer branches on that field only — never on node state.
2. **In deployer mode the `site-root` `index.php` artifact is not emitted at
   all** (`internal/platform/operators/sites/render.go:322`). This single
   decision removes every plan-drift hazard: `checkBefore`'s digest gate
   (`artifacts.go:48-78`) never sees a path that an external deploy can flip,
   `Rollback`'s "managed site changed after activation" refusal
   (`activation.go:172-184`) can never fire on a release file, and teardown keeps
   working. The pre-deploy placeholder is instead seeded **unmanaged and
   idempotently, only when absent**, by `PrepareSite`.
3. **`documentRoot()` branches on the mode**:
   `{root}/current/public[/<subdirectory>]` for deployer, unchanged otherwise
   (`internal/platform/operators/sites/render_support.go:85-94`).
4. **`disable_symlinks if_not_owner from={releaseRoot}`** (superseded 2026-07-22).
   The original plan anchored the `from=` prefix at the document root so the
   `current` component would fall inside it and be exempt from the ownership
   check, avoiding a 403 on the root-created link over site-owned releases.
   That is wrong: `{root}/app` is site-owned by design, so the exemption let a
   site re-point `current` anywhere on the host and have nginx (www-data) serve
   it — a cross-site arbitrary-file read over HTTP. The prefix is therefore the
   **release root** (`{root}/app`), which puts `current` inside the checked
   region, and `PrepareSite` **lchowns the seeded link to the site account** so
   the legitimate initial state (site-owned link over a site-owned release)
   passes. See `symlinkFrom()` in `render_support.go`.
5. **`VerifyHost` gains a real doc-root assertion.** Today a dangling root serves
   404 and passes (`operators/sites/host_system.go:358-377`). Add a node-side
   `VerifyDocumentRoot(ctx, site) error` step in `Apply` **before**
   `ValidateNginx`, that `os.Stat`s the resolved document root and fails when it
   is missing or not a directory. This is a host check, not a render change, so
   byte-exactness is untouched, and it closes a pre-existing bug for standard
   sites too.
6. **PHP-FPM `chdir`** must also point at `{root}/current` in deployer mode
   (`render_support.go:174-208`), or FPM starts in a directory that release
   rotation leaves stale. Same purity rules apply.

### 3.1 Work items

---

**P3-A · `PARALLEL-GROUP P3-A` — schema + control-panel field**

`migrations/20260722000005_site_deployment_mode.tx.up.sql`:

```sql
ALTER TABLE sites ADD COLUMN deployment_mode TEXT NOT NULL DEFAULT 'standard';
```
Down: `ALTER TABLE sites DROP COLUMN deployment_mode;` (SQLite supports this;
`migrations/20260721000027_site_settings.tx.down.sql:1` is the precedent).

Edit:
- `internal/modules/sites/sites.go:106-130` — add `DeploymentMode string` to `siteModel` and to the exported `Site` (`json:"deploymentMode"`).
- `internal/modules/sites/definition.go:24-38` — thread it into `siteoperator.Site`.
- `internal/modules/sites/lifecycle.go:37` — default `"standard"` at create.
- `internal/modules/sites/support.go` — `func validDeploymentMode(v string) bool` accepting exactly `standard` | `deployer`.

---

**P3-B · `PARALLEL-GROUP P3-B` — renderer (depends on P3-A's operator field)**

Edit `internal/platform/operators/sites/render.go`:
- Add `DeploymentMode string \`json:"deploymentMode"\`` to `Site`.
- `Renderer.validate` (`:347-408`): reject any value other than `""`/`standard`/`deployer` (empty normalises to standard so existing plans stay byte-identical).
- `:322` — emit the `site-root` artifact **only** when the mode is not `deployer`.

Edit `internal/platform/operators/sites/render_support.go`:
- `documentRoot(site)` (`:85-94`) — prepend `current` in deployer mode.
- `fpmDataFor` (`:174-208`) — `chdir` becomes `{root}/current` (plus `WorkingDirectory`) in deployer mode.

Golden tests in `internal/platform/operators/sites/` covering both modes and
asserting that a `standard`-mode render is **byte-identical to today's output**
(this is the regression that matters — every existing site must re-plan to the
same bytes).

---

**P3-C · `PARALLEL-GROUP P3-C` — node-side layout (depends on P3-B's `Site` field)**

Edit `internal/platform/operators/sites/host_system.go`:

```go
// prepareDeployerLayout creates releases/ and shared/ and seeds the initial
// release plus the current symlink. Everything it creates is idempotent and
// unmanaged: Deployer owns the release tree afterwards and the panel never
// re-asserts ownership below releases/.
func prepareDeployerLayout(root *os.Root, site Site, uid, gid int) error
```

Called from `PrepareSite` (`:113`) when `site.DeploymentMode == "deployer"`,
before `prepareDocumentRoot`. It creates, via the existing
`prepareOwnedDirectory` helper (`:145-175`, all `nexa_<slug>:www-data`):

| path | mode |
|---|---|
| `releases` | `0755` |
| `releases/initial` | `0755` |
| `releases/initial/public` | `0750` |
| `shared` | `0750` |
| `shared/storage` | `0750` |

then seeds `releases/initial/public/index.php` **only when absent** (best-effort,
`0640 nexa_<slug>:www-data`), then creates the `current` symlink via a new
helper:

```go
// prepareCurrentSymlink installs {root}/current -> releases/initial when the
// link is absent. An existing symlink is left exactly as Deployer set it; a
// non-symlink at that path is a hard error, mirroring prepareOwnedDirectory's
// refusal to adopt an unmanaged entry.
func prepareCurrentSymlink(root *os.Root, target string) error
```

`prepareOwnedDirectory`'s blanket symlink rejection (`:156`) is **not relaxed** —
`current` is never passed to it. Instead `prepareCurrentSymlink` does its own
`Lstat`: absent → `Symlinkat` to `releases/initial`; symlink → leave alone;
anything else → error `"current is not a managed release link"`. Root creates the
link because the site root is root-owned; Deployer replaces it atomically at
deploy time via `ln -sfn` inside `{root}` — **which the site user cannot do**.

> **RESOLVED 2026-07-22 — option (b) adopted.** The release tree is nested at
> `{root}/app`, owned `nexa_<slug>:nexa_<slug>`, so `{root}` stays `root:root 0755`
> and the SFTP chroot invariant is untouched. `deploy_path` = `{root}/app`;
> `documentRoot` = `{root}/app/current/public`; `releases/`, `shared/` and
> `current` all live under `{root}/app`. Substitute `{root}/app/…` for `{root}/…`
> throughout Phase 3. The original analysis is retained below for context.
>
> **OPEN QUESTION (RESOLVED — see above).** Deployer's
> `deploy:symlink` task runs as the deploy user and must atomically re-point
> `{root}/current`, but `{root}` is `root:root 0755` and must stay that way for
> the SFTP chroot. Three candidate resolutions, none obviously best:
> (a) set the setgid+sticky bits and grant the site group write on `{root}` —
> **rejected**, sshd refuses a group-writable `ChrootDirectory`;
> (b) move the release layout one level down (`{root}/app/{releases,shared,current}`)
> with `app` owned by the site user and `root {root}/app/current/public` —
> keeps the chroot invariant intact and lets Deployer work unmodified; costs one
> extra path segment and makes `deploy_path` `= {root}/app`;
> (c) give Deployer a panel-provided `symlink` hook that calls back into the
> agent. **Recommendation: (b).** It is the only option that satisfies the SFTP
> invariant, needs no Deployer customisation, and needs no new privileged
> mechanism. If (b) is adopted, substitute `{root}/app/…` for `{root}/…`
> throughout this phase and set `documentRoot` to `{root}/app/current/public`.

Also edit:
- `internal/platform/operators/sites/host_system.go:177-231` — `SecureArtifacts`
  must be left alone; confirm with a test that no artifact path can resolve
  inside `releases/` (it cannot, since the `site-root` artifact is no longer
  emitted in deployer mode).
- Add `func (s *HostSystem) VerifyDocumentRoot(ctx context.Context, site Site) error`
  and call it in `internal/platform/operators/sites/activation.go` between
  `setEnabled` and `ValidatePHP`. Add it to the `NodeSystem` interface and every
  fake in the test files.

---

**P3-D · `PARALLEL-GROUP P3-D` — mode switch + `.env` editor (depends on P3-A..C)**

Control panel, `internal/modules/deploy/layout_http.go`:

| Pattern | Permission |
|---|---|
| `PATCH /api/v1/sites/{id}/deployment-mode` | `deploy.write` → 202 `{"job": …}` |
| `GET /api/v1/sites/{id}/deployment/env` | `deploy.read` |
| `PUT /api/v1/sites/{id}/deployment/env` | `deploy.write` |

The mode switch persists `deployment_mode` and then submits the existing
`site.settings` job path so the site re-renders and re-applies — reuse
`internal/modules/sites/operations.go:103-164` rather than inventing a second
apply path. Gate on the existing status predicate
(`internal/modules/sites/support.go:301-309`, `settingsEditable`); a site that is
not editable returns 409 `site_busy` (`handlers.go:174`).

The `.env` editor is **synchronous**, through a new operator pair:

```go
func (o *SSHHostOperator) ReadSharedEnv(ctx context.Context, r EnvRequest) (EnvDocument, error)
func (o *SSHHostOperator) WriteSharedEnv(ctx context.Context, r EnvRequest, content string) (EnvDocument, error)
```

Path `{root}/shared/.env` (or `{root}/app/shared/.env` under resolution (b)),
mode `0640`, owner `nexa_<slug>:www-data`, written with `atomicWriteOwned`-style
chown-on-fd (`operators/sites/artifacts.go:233`). Cap the document at 64 KiB and
decode the request body with an explicit `httpapi.DecodeJSONLimit` — the default
is 16 KiB (`internal/platform/httpapi/httpapi.go:17,34`). Reject `\x00`. Audit
`deploy.env_updated` with `bytes` and `sha256` only, **never content**.

Frontend: extend `SiteDeploymentView.vue` with a mode switch (an
`AppConfirmDialog`, since it re-applies the vhost) and an `.env` editor using the
lazily-imported Monaco editor
(`defineAsyncComponent(() => import('@/shared/ui/MonacoEditor.vue'))`,
language `ini`, `save` on Ctrl/Cmd-S), draft/dirty pattern from
`SiteSettingsCard.vue:24-97`.

### 3.2 Verification

Automated:
- Golden render tests proving `standard` mode output is byte-identical to the
  pre-change output (add a fixture captured before the change).
- `PrepareSite` tests: idempotency across two runs; refusal when `current` exists
  as a regular file; leaving an existing `current` symlink untouched; correct
  modes/owners on `releases`, `shared`, `shared/storage`.
- `VerifyDocumentRoot` test: a deployer site with a dangling `current` fails
  activation instead of passing with a 404 (this is the regression the current
  probe cannot catch).
- Teardown test: deleting a deployer-mode site succeeds even after the release
  tree has been rotated by an external process.

Manual: switch a site to deployer mode, confirm `nginx -T | grep root` shows
`.../current/public`, confirm the site serves the placeholder over HTTP before
any deploy, then hand-rotate `current` to a second release directory and confirm
the site serves the new content without a panel action and that a subsequent
`site.settings` re-apply still succeeds.

---

## Phase 4 — Server prerequisites

**Goal:** a one-click "Prepare this node for deployments" job; a narrow mechanism
letting the site user reload its own FPM pool; and a firewall check that port 22
is open.

### 4.1 Work items

---

**P4-A · `PARALLEL-GROUP P4-A` — installer + docs**

Edit `scripts/install.sh:173-184` — add a continuation line to the single
prerequisites block: `git unzip rsync acl sudo`.

Caveats already established:
- This block runs **before** `ppa:ondrej/php` is added (`install.sh:233-234`) and
  before the second `apt-get update` (`install.sh:243`). **Do not add `composer`
  here** — Ubuntu's archive `composer` drags in distro `php-cli` 8.3 and would
  make the Applications catalog entry (`internal/platform/operators/packages/catalog.go:186-189`)
  permanently read as installed. Composer stays an on-demand install driven by
  the prepare job (P4-B).
- `--no-install-recommends` is in effect.
- `sudo` is needed only for P4-C; if P4-C's sudoers approach is rejected, drop it.

Edit `packaging/REQUIREMENTS.md` — document the four new packages under
"Installed foundation".

Optionally add a preflight check: `internal/platform/preflight/preflight.go:115-129`
is a fixed `Checks` list with **no binary-presence check of any kind** today. A
`deploy-tooling` check that reports missing `git`/`rsync` would be the first of
its kind; treat as nice-to-have, not a gate.

---

**P4-B · `PARALLEL-GROUP P4-B` — prepare job**

Operator, `internal/platform/operators/deploy/prepare.go`:

```go
type PrepareRequest struct {
	PHPVersion string `json:"phpVersion"` // the site's branch, for php-cli
}

type ToolStatus struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Installed bool   `json:"installed"`
	Action    string `json:"action"` // "present" | "installed" | "failed"
}

type PrepareObservation struct {
	Tools       []ToolStatus `json:"tools"`
	FirewallSSH FirewallSSH  `json:"firewallSsh"`
	Warnings    []string     `json:"warnings"`
}

func (o *SSHHostOperator) Prepare(ctx context.Context, r PrepareRequest) (PrepareObservation, error)
```

Required set: `git`, `unzip`, `rsync`, `setfacl` (from `acl`), `composer`,
`php<version>-cli`. For each, `exec.LookPath`; when missing, `apt-get install -y
--no-install-recommends <pkg>` under a serializing `applyMu`. The package name
list is a **hardcoded map**, never derived from the request — the request only
carries a PHP version, itself validated against installed branches the way
`operators/php/planning.go:47` does.

This endpoint gets its **own 30-minute `UnixClient`** (the PHP operator
precedent, `operators/php/client.go:20-22`); the agent's `WriteTimeout` is
already 35 minutes (`internal/platform/agent/server.go:200`).

Firewall check: the deploy module holds a `firewalloperator.Operator` (a second
`NewUnixClient` constructed in `cmd/nexa/api.go`) and calls `Discover(ctx)`.
Port 22 is "open" iff `Status.Active` is false (firewall off → nothing blocked,
report as a warning not a pass) **or** a rule exists with `Port == "22"` and
`Action == "allow"`. Protocol may be `"tcp"` or `""`, and UFW emits a separate
`V6: true` row (`internal/platform/operators/firewall/system.go:51-59`). This is
**read-only**: the prepare job never opens a port. If 22 is closed it emits a
warning with the exact `ufw allow 22/tcp` remediation. Rationale: opening a
firewall port must stay behind the MFA-gated `firewall.write` path.

Control panel: register `queue.RegisterHandler("deploy.prepare", m.prepareJob)`
in the module constructor; route
`POST /api/v1/sites/{id}/deployment/prepare` → `deploy.write` → 202. Audit
`deploy.node_prepared` fail-closed at submit time.

---

**P4-C · `PARALLEL-GROUP P4-C` — narrow FPM reload (SECURITY DECISION)**

The site user must be able to reload its own pool after a release swap.
`systemctl reload` needs root, and the repo has **no sudoers file anywhere**.
Two artifacts, both root-owned:

1. Wrapper `/etc/nexa-panel/generated/deploy/nexa-fpm-reload-<slug>.sh`,
   mode `0555`, owner `root:root` — deliberately **not** writable by anyone,
   unlike the schedules wrapper (`0750 root:<unixUser>`,
   `operators/schedules/render.go:54`) which the site user must not be able to
   edit here because it runs as root. Content is fixed and takes **no arguments**:

   ```sh
   #!/bin/sh
   # Managed by Nexa Panel. Reloads the PHP-FPM master serving <slug>.
   set -eu
   exec /usr/bin/systemctl reload php<version>-fpm.service
   ```

2. Sudoers drop-in `/etc/sudoers.d/nexa-deploy-<slug>`, mode `0440 root:root`:

   ```
   nexa_<slug> ALL=(root) NOPASSWD: /etc/nexa-panel/generated/deploy/nexa-fpm-reload-<slug>.sh
   ```

   Single absolute path, no wildcards, no `ALL` command, no environment
   passthrough. Written atomically to a temp file, validated with
   `visudo -cf <temp>` **before** the rename, and removed if validation fails —
   the same install-validate-rollback discipline as the sshd drop-in
   (`operators/sftp/host.go:57-64`). A malformed sudoers file breaks `sudo`
   host-wide, so the pre-rename validation is mandatory, not optional.

   `/etc/nexa-panel/generated/deploy` must be added to
   `packaging/tmpfiles/nexa-panel.conf` as `d /etc/nexa-panel/generated/deploy 0711 root root -`
   (the existing `…/generated/tasks` entry is the model).
   `packaging/security_contract_test.go:288-311` asserts a **subset**, so adding
   an entry is safe.

**Accepted limitation:** PHP-FPM has no per-pool reload; `systemctl reload
php<ver>-fpm.service` gracefully reloads every pool on that branch. The wrapper
is still "narrow" in the sense that matters — the site user can invoke exactly
one fixed command with no arguments — but it is not pool-scoped.

> **OPEN QUESTION.** Making the reload genuinely pool-scoped would require
> running each site's pool as its own systemd unit (`php-fpm --fpm-config` per
> site) instead of a branch-wide master, which is a large change to
> `internal/platform/operators/sites/render.go:323,716-737` and to every reload
> path (`host_system.go:311-317`). Out of scope here; record it as follow-up.
>
> **OPEN QUESTION.** Whether the wrapper + sudoers pair should be created for
> every site or only for deployer-mode sites. Recommendation: only on the prepare
> job for deployer-mode sites, and removed by the same teardown blocker work item
> as Phase 1's drop-in, so the sudoers surface stays proportional to actual use.

---

**P4-D · `PARALLEL-GROUP P4-D` — frontend + docs**

- Extend `SiteDeploymentView.vue` with a "Server prerequisites" card: a "Prepare
  this node" button driving `useJobRunner`, a tool checklist rendered from the
  job result, and an amber `AppAlert` when the firewall check reports port 22
  closed, linking to `/firewall`.
- `openapi/openapi.yaml` + new `openapi/paths/deploy.yaml` +
  `openapi/components/schemas/deploy.yaml` covering all Phase 1–4 routes in one
  batch. Optional (sftp/firewall/services ship without a spec), but this is the
  natural point to do it since `openapi-lint` only checks what exists.
- `CONTEXT.md` glossary entries (0.6) and the `PLAN.md` milestone move (0.6).
- A runbook at `docs/runbooks/` covering: rotating a deploy key, recovering a
  site whose `current` symlink is broken, and removing the sudoers drop-in by hand.

### 4.2 Verification

Automated: operator tests with a fake runner asserting the exact `apt-get` argv
and that the package list cannot be influenced by the request; a sudoers-render
golden test; a test that a failing `visudo -cf` leaves no file behind; a firewall
check test over recorded `ufw status verbose` output including the IPv6 duplicate
row and the "inactive" case.

Manual, on the test node:
1. Run the prepare job on a fresh node, watch progress, confirm `git --version`,
   `rsync --version`, `composer --version`, `setfacl --version` all resolve.
2. `sudo -l -U nexa_<slug>` lists exactly the one wrapper path.
3. `runuser -u nexa_<slug> -- sudo -n /etc/nexa-panel/generated/deploy/nexa-fpm-reload-<slug>.sh`
   succeeds; `runuser -u nexa_<slug> -- sudo -n /bin/sh` is refused.
4. `ufw delete allow 22/tcp`, re-run the prepare job, confirm the warning appears
   and that the job does **not** re-open the port.
5. Full end-to-end: point a real Deployer `deploy.php` at the site
   (`deploy_path` per the P3-C resolution) and run `dep deploy`.

---

## 5. Cross-phase ordering summary

```
Phase 1 ──→ Phase 2 ──→ Phase 4
    │
    └──────→ Phase 3 ──→ Phase 4
```

Phase 2 needs Phase 1's `.ssh` directory and module skeleton. Phase 3 is
independent of Phase 2 and can be built in parallel by a separate agent — the two
touch disjoint files apart from `internal/modules/deploy/deploy.go` (route
registration) and `SiteDeploymentView.vue`; serialize those two files or split
route registration into per-feature `*_http.go` files (recommended, and assumed
above). Phase 4 depends on Phase 3 only for which sites get the wrapper.

Each phase is independently shippable: Phase 1 alone gives per-site SSH; Phase 2
alone gives a working GitHub deploy key usable by any manual workflow; Phase 3
alone gives a release-shaped site servable by rsync; Phase 4 alone is a
prerequisites checker.

## 6. Consolidated open questions

1. ~~**P3-C resolution (a)/(b)/(c)**~~ — **RESOLVED 2026-07-22: option (b).** The
   release tree lives at `{root}/app/{releases,shared,current}`, owned by the site
   user, leaving `{root}` root-owned for the SFTP chroot. `deploy_path` =
   `{root}/app`, `documentRoot` = `{root}/app/current/public`.
2. **GitHub host key blobs and fingerprints** must be transcribed and
   independently verified at implementation time; the values in 2.1 are
   indicative only.
3. **Whether to write `~/.ssh/config` with `CheckHostIP no`** rather than pinning
   GitHub IP ranges in `known_hosts`. Recommendation: yes.
4. **Per-site FPM units** to make the reload genuinely pool-scoped — deferred.
5. **Sudoers scope per site vs global** — recommendation: per deployer-mode site,
   removed at teardown.
6. **Teardown extension point** — `dependentBlocker` (refuse deletion while SSH
   access is enabled) vs extending `deleteJob`'s hardcoded cleanup. Recommendation:
   blocker, matching how domains and certificates already behave.
7. **Whether "streamed to the job runner" requires line-level streaming** of
   `ssh -T` / `git ls-remote` output. This plan implements staged `report()`
   messages plus a captured 64 KiB tail in the job result. True line-level
   streaming would need the agent to chunk output back over the Unix socket,
   which no existing operator does.
