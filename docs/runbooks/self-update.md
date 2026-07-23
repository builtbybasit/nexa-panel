# Self-update

How a node moves itself from one panel release to the next, what it needs to be
able to do so, and what to do when it half-works.

## What a release is

A release is not a binary. It is a per-architecture tarball published on
`github.com/builtbybasit/nexa-panel`:

```
nexa-panel-linux-<arch>.tar.gz            # asset
nexa-panel-linux-<arch>.tar.gz.sha256     # sidecar checksum

nexa-panel-<version>-linux-<arch>/
  bin/nexa
  packaging/                              (full tree)
  scripts/install.sh
  scripts/nexa-seed-admin.sh
  RELEASE                                 (version, commit, arch, built_at)
```

A panel version is the binary *and* its packaging — systemd units, tmpfiles and
sysusers rules, the nginx template, host prerequisites. Swapping only
`/usr/bin/nexa` half-upgrades a node: a release that adds a tmpfiles directory,
or widens `nexa-agent.service`'s `ReadWritePaths`, then fails at runtime with
nothing to explain why. So a self-update applies both.

## The release token

The release repository is private, so both the release metadata and the release
assets are 404s to an anonymous client.

| | |
|---|---|
| Path | `/etc/nexa-panel/release.token` |
| Mode | `0600`, root-owned. Anything group- or world-readable is **refused**, not used |
| Contents | the raw token, trailing newline permitted |
| Override | `NEXA_RELEASE_TOKEN` in the agent's environment |
| Optional? | yes — with no token the same requests are issued unauthenticated |
| Written by | `scripts/install.sh` (from `--github-token-file` / `NEXA_GITHUB_TOKEN`) |

The token is re-read for every operation, so rotating it is `install -m 0600 -o
root -g root newtoken /etc/nexa-panel/release.token` — no agent restart. It is
never written to a log line, an error, a job payload, or an API response.

Two failures that look alike and are not:

- **`release token missing or invalid`** (HTTP 401/403) — the node has a token
  and the repository rejected it. Expired, revoked, or lacking read access.
- **`no matching release was published, or this node's release token cannot see
  it`** (HTTP 404) — either that version genuinely does not exist, or the node
  has no token at all, since a private repository answers 404 to strangers.

Check with `sudo nexa self-update --check`.

## What an update does

1. Resolve the release from the trusted repository (compile-time constant; no
   caller can redirect it). Drafts and pre-releases are refused, and only a
   strictly newer version is accepted.
2. Download the tarball and its `.sha256`; verify.
3. Extract to a staging directory under `/var/lib/nexa-panel-update`, treating
   the tar as hostile input: no absolute or traversing paths, no symlinks,
   hardlinks or devices, nothing outside the known release layout, size and
   entry caps.
4. Validate `bin/nexa` — it must run and report the expected version — then
   swap it atomically over `/usr/bin/nexa`, preserving the displaced binary as
   `/usr/bin/nexa.prev`.
5. Run the extracted `scripts/install.sh --sync-packaging --no-start` to apply
   units, tmpfiles, sysusers, the nginx template and any new prerequisites.
   `--no-start` matters: on its own the installer restarts `nexa-agent`, which
   would kill the very RPC waiting on it.
6. Retain the applied tree as `.../update/current`, demoting the previous one to
   `.../update/previous`.
7. Arm a detached, delayed `systemd-run` restart of `nexa-agent` and `nexa-api`,
   and return before it fires so the job can record success.

`/var/lib/nexa-panel-update` is root-owned `0700` and deliberately *not* under
`/var/lib/nexa-panel`, which belongs to the unprivileged `nexa` account: the
tree holds an `install.sh` the agent executes as root.

## Rollback

`sudo nexa self-update rollback` restores `/usr/bin/nexa.prev` **and**, when the
previous release tree is still retained, re-applies its packaging. The two are
one artifact; reverting only the binary onto the newer release's units is the
same half-upgrade in the other direction.

The rollback is itself undoable: it re-preserves the binary it displaced as the
new `.prev` and swaps the retained `current`/`previous` trees.

**The one case it cannot cover**: a node's *first* self-update. The packaging it
was installed with was never captured, so there is nothing to restore. The
binary still goes back and the result says so:

> only the binary was rolled back: this node has no retained packaging for the
> previous version, so its systemd units, tmpfiles rules and nginx template are
> still the newer release's

From the second self-update onwards, rollback is complete.

`nexa self-update --binary PATH` (and `scripts/self-update-push.sh`) is likewise
binary-only by construction — it pushes a build, not a release. It says so on
every run.

## When it half-works

If the binary swap succeeds and `--sync-packaging` fails, the update reports an
error and arms **no** restart. The node is running the new binary with the old
packaging. Either:

- `sudo nexa self-update rollback`, or
- fix whatever the installer complained about and re-run
  `/var/lib/nexa-panel-update/current/scripts/install.sh --sync-packaging` by
  hand.

## Unit requirements

`nexa-agent.service` runs `ProtectSystem=strict` with an explicit
`ReadWritePaths`. Both paths this feature needs are already inside it:
`/etc` covers `/etc/nexa-panel/release.token`, and `/var` covers
`/var/lib/nexa-panel-update`. No unit change is required.
