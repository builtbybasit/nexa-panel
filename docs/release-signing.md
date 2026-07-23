# Release signing, provenance, and the release token

Two independent secrets sit behind a panel release, and keeping them independent
is the point:

- the **release signing key** proves a bundle was published by this project. Its
  public half is pinned on every node; its private half exists only as the
  GitHub Actions secret `NEXA_RELEASE_SIGNING_KEY`.
- the **release token** proves a node is allowed to *download* from the private
  release repository. It is a read-only credential on every managed host.

A stolen release token lets someone read releases they could otherwise not read.
A stolen signing key lets someone publish a release every node will install as
root. They are never the same secret, never stored in the same place, and are
rotated on different triggers.

## What a release publishes

Per architecture:

| Asset | Purpose |
| --- | --- |
| `nexa-panel-linux-<arch>.tar.gz` | the installable bundle |
| `nexa-panel-linux-<arch>.tar.gz.sha256` | the digest the installer and updater verify |
| `nexa-panel-linux-<arch>.tar.gz.sig` | detached OpenSSH signature over the bundle |
| `nexa-panel-linux-<arch>.tar.gz.spdx.json` | SPDX SBOM of the bundle |
| `nexa-panel-linux-<arch>.tar.gz.spdx.json.sig` | detached signature over the SBOM |

Every published file is signed. The bare `nexa-linux-<arch>` binary is **not**
published: it carried only a `.sha256`, and an unsigned artifact sitting beside
signed ones is a trap — the tempting thing to `curl` is the one thing whose
authenticity nothing proves. It also cannot be installed from on its own, because
`scripts/install.sh` reads the packaging tree out of the bundle it ships in. It
remains a local build product of `scripts/build-linux-release.sh` for the Docker
test node and `nexa self-update --binary`.

The workflow also asks GitHub for a **build-provenance attestation** and an **SBOM
attestation**, signed with the workflow's OIDC identity:

```bash
gh attestation verify nexa-panel-linux-amd64.tar.gz --repo builtbybasit/nexa-panel
```

**Those two steps are skipped while this repository is private.** GitHub refuses
to persist attestations for user-owned private repositories — "Feature not
available for user-owned private repositories. To enable this feature, please
make this repository public" — and going public is not on the table, because the
release token exists precisely so that nodes can download from a *private*
release. The steps are guarded on the repository's visibility and return on their
own if it is ever made public, so the command above only works then.

Nothing about the trust boundary changes in the meantime: every asset is still
signed with the pinned release key, the workflow verifies each signature against
`packaging/release-signers` before publishing, and the installer and self-updater
reject a bundle whose signature is missing or invalid. The attestation is
supplementary provenance, not the check that gates an install.

## The signing key

Generate it on a trusted offline workstation:

```bash
ssh-keygen -t ed25519 -C nexa-panel-release -f nexa-panel-release-signing
```

Store the complete private-key file as the repository Actions secret
`NEXA_RELEASE_SIGNING_KEY` and commit only this allowed-signers line to
`packaging/release-signers`:

```text
nexa-panel-release ssh-ed25519 <public-key> nexa-panel-release
```

The release workflow signs each asset with namespace `file`, verifies it against
the pinned allowed-signers file before publishing, and ships the `.sig`. The
installer and self-updater reject a missing, duplicate, or invalid signature even
when the SHA-256 sidecar matches.

To rotate, first publish a release whose `packaging/release-signers` contains
both public keys. Once supported nodes have installed that overlap release, sign
with the new key and drop the old public key in a later release. A suspected
private-key exposure requires an immediate release freeze and incident review; do
not simply replace the public key in an unsigned bootstrap.

## The release token

### Creation

Use a **fine-grained personal access token**, scoped to the single release
repository, with exactly one permission:

- Resource owner: the account or organization that owns `builtbybasit/nexa-panel`
- Repository access: **Only select repositories** → `nexa-panel`
- Repository permissions: **Contents: Read-only**. Nothing else — no Actions, no
  Metadata beyond the mandatory read, no write anywhere.
- Expiration: **90 days maximum**. A token that never expires is a token nobody
  ever notices the loss of.

Never use a classic `repo` token: its scope covers every repository the account
can reach, including private ones this project has no business touching, and it
cannot be narrowed. Never use a human's day-to-day GitHub credential — the token
must be revocable without disrupting a person.

For a fleet, prefer one token per node or per small group, so a compromised host
is revoked without re-provisioning every other host.

### Installation

The token goes to the node as a *file*, never on a command line and never in an
environment variable that lands in a shell history or a process listing:

```bash
sudo install -m 0600 -o root -g root /path/to/token /etc/nexa-panel/release.token
```

`scripts/install.sh --github-token-file PATH` does this as part of an install.
The updater re-reads `/etc/nexa-panel/release.token` on every operation, so
rewriting the file rotates the credential with no service restart. It refuses to
use the file unless it is a regular file, root-owned, and mode `0600`, and it
never puts the token's contents into an error, a log line, a job payload, or an
API response. The unprivileged API and Nginx identities have no access to it: the
file is read only by the root agent.

`NEXA_RELEASE_TOKEN` overrides the file for a one-off run by an operator. It is
read from the agent's own environment, never from an RPC. Do not use it as the
persistent mechanism.

### Backup exclusion

**`/etc/nexa-panel/release.token` must never appear in a backup.** Panel backups
capture site data, databases and configuration, and those archives routinely
leave the host for object storage that is not held to the same standard as
`/etc`. A live read-only credential inside a backup means every copy of that
backup — and everyone who can read the bucket — is a copy of the credential, long
after the token has been rotated on the node itself.

If a backup destination is added by hand, exclude the path explicitly. If a token
is found in an existing archive, treat it as an exposure and follow the incident
response below; deleting the archive is not sufficient, because you cannot prove
it was never read.

The same exclusion applies to any snapshot, image, or golden AMI built from a
node that has been installed: bake the panel, then install the token, never the
other way round.

### Expiry

A fine-grained token expires on the date it was created with, and GitHub emails
the owner beforehand. From the node's side the symptom is unambiguous: the
updater reports *"this node's release token was rejected: it has expired, been
revoked, or does not belong to the release repository"*. That message means
replace the credential — it never means retry, and it is deliberately distinct
from:

- *"this node has no release token"* — nothing is installed; install one.
- *"valid but lacks read-only Contents access"* — the token is live but was
  created with the wrong permission; re-issue it with Contents: Read-only.
- *"the release source is rate limiting this node"* — the credential is fine.
  Wait; the message says for how long. Rotating in response to this fixes
  nothing and burns a token.

Schedule rotation for two weeks before expiry so an update check never fails on a
date nobody was watching.

### Rotation

1. Create the replacement token first, with the same narrow scope.
2. Write it over `/etc/nexa-panel/release.token` on each node (`install -m 0600
   -o root -g root`). The agent picks it up on the next operation; no restart.
3. Confirm with `nexa self-update --check` on a representative node.
4. **Then** revoke the old token in GitHub. Revoking first leaves a window in
   which nodes cannot check for updates.

Rotate on a schedule (at minimum before every expiry), and immediately on any
personnel change that touched the credential.

### Revocation

Revoke at *Settings → Developer settings → Personal access tokens →
Fine-grained tokens → Delete*. Revocation is immediate and global; a revoked
token produces the same "rejected" message as an expired one, so plan on
replacing the file on every node that used it.

Because a fine-grained token grants only read-only Contents on one repository, a
revoked-but-uncollected token on a decommissioned host is a small exposure — but
it is still an exposure, so revoke as part of decommissioning, not after it.

### Incident response

If a release token leaks (found in a backup, a log, a screenshot, a chat
message, a snapshot image, or on a host that was compromised):

1. **Revoke the token immediately.** Read-only or not, it is an authenticated
   view of private source releases.
2. Create and distribute a replacement to every node that shared it. This is the
   argument for per-node tokens: with a shared token, one leak re-provisions the
   fleet.
3. Review the repository's audit log for downloads that were not this fleet.
4. Ask whether the same exposure carried anything else — a token in a backup
   usually means the whole `/etc/nexa-panel` tree was in that backup, including
   the agent token and any other secret alongside it.
5. Record what leaked, how, and what changed so the same path is closed.

A leaked **release token** does not compromise release authenticity: it cannot
publish, and every bundle is signature-verified against the pinned key before a
node extracts or executes anything. Escalate to the signing-key procedure above
only if `NEXA_RELEASE_SIGNING_KEY` itself may have been exposed — that is a
release freeze, not a rotation.
