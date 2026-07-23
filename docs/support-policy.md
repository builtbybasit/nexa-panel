# Support and compatibility policy

Nexa Panel currently supports Ubuntu 24.04 LTS on Linux AMD64 and ARM64. The
packaged systemd/Nginx topology is the production target; Docker is acceptance
infrastructure only.

Before `v1.0.0`, only the latest development revision is maintained and no
production-support promise is made.

From `v1.0.0` the supported window is the current minor (N) and the immediately
previous minor (N-1). N receives security and bug fixes; N-1 receives security
fixes only, for 90 days after N is tagged. Older minors are unsupported. Fixes
ship as a new patch release of a supported minor and are never backported to an
earlier patch, so only the latest patch of N and N-1 is served.

The upgrade window follows from that: a node must move from N-1 to N within
those 90 days, and must upgrade one minor at a time — skipping a minor is
untested and unsupported. Security reports are acknowledged within three
business days and triaged within seven; fixes target 7 days for critical, 14 for
high, 30 for moderate, and the next scheduled release for low severity, counted
from triage. Reporting instructions and the full statement of these targets are
in [SECURITY.md](../SECURITY.md).

Database migrations must be expand/contract compatible with the immediately
previous supported binary. A migration that prevents N-1 from starting must
declare itself irreversible and block activation until an independent state
backup and explicit operator acknowledgement exist. Release qualification must
exercise both forward migration and rollback from N-1.

Private GitHub release access is operational configuration, not a product
license. Nodes should use a fine-grained, repository-scoped, read-only Contents
token with an expiry and documented owner. A missing/expired token must stop an
update check without affecting the running version.
