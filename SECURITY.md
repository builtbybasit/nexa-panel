# Security policy

## Supported versions

Nexa Panel is pre-release. Until `v1.0.0` is tagged, only the current default
branch receives security fixes and it is not supported for an Internet-facing
production server.

From `v1.0.0` the support window is:

| Version | Status |
| --- | --- |
| Current minor (N) | Supported: security and bug fixes |
| Previous minor (N-1) | Supported: security fixes only, for 90 days after N is tagged |
| Older minors | Unsupported |

Only the latest patch of a supported minor is served — a fix is published as a
new patch release, never backported to an earlier patch. Nodes must therefore
upgrade from N-1 to N within 90 days, and must upgrade one minor at a time;
skipping a minor is not a tested path. The same policy is stated, with the
migration and rollback requirements it implies, in
[docs/support-policy.md](./docs/support-policy.md).

The supported host target is Ubuntu 24.04 LTS on Linux AMD64 or ARM64. Reports
that reproduce only on another distribution are welcome, but are not treated as
supported-platform vulnerabilities until confirmed on that target.

## Reporting a vulnerability

Do not open a public issue. Use the private repository's **Security → Report a
vulnerability** flow so the report, patches, and advisory remain confidential.
Include the affected version/commit, impact, reproduction steps, and any known
workaround. Never include production credentials, private keys, database dumps,
or customer data.

Maintainers acknowledge a complete report within three business days and triage
severity within seven days. Fix targets, counted from triage, are 7 days for
critical, 14 days for high, 30 days for moderate, and the next scheduled release
for low severity. Disclosure is coordinated only after a fix and upgrade
guidance exist for every supported version, and the advisory is published with
that release. These are response targets, not a paid SLA.

If a release credential may be exposed, revoke it at the provider immediately,
replace `/etc/nexa-panel/release.token` atomically on every affected node, and
review the panel audit chain plus host logs before resuming updates.

## Security boundaries

The public Nginx worker is untrusted. It may reach only the unprivileged API
socket. The API is the authenticated policy boundary and is the only service
allowed to reach the root agent socket. Release archives require a pinned
publisher signature in addition to a checksum; checksums alone are not an
authenticity guarantee.

Security fixes must include a regression test at the boundary that failed, not
only a unit test of the replacement implementation.
