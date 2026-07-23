# Changelog

All notable changes are recorded here. Nexa Panel follows Semantic Versioning
once stable releases begin.

## Unreleased

### Added

- Signed AMD64 and ARM64 install bundles with pinned OpenSSH verification.
- Safe release JSON parsing and bounded archive extraction.
- Retain-data uninstall, explicit purge mode, and Docker lifecycle acceptance.
- Mandatory administrator MFA enrollment and recovery paths.

### Changed

- Installation is loopback-first; public plaintext and UFW rule management now
  require explicit operator consent.
- Self-update uses a journaled activation transaction and health-gated success.
- Browser-session changes clear cached server data and centralized 401 handling
  expires the local session.

### Security

- Nginx no longer receives the agent group or token and cannot connect to the
  privileged agent socket.
- Local binary update is no longer exposed through the network agent RPC.

### Fixed

- Metrics forwarding through packaged Nginx.
- Site deletion ownership of schedules and backup references.
- pgAdmin proxy path/session behavior and destructive UI safety checks.

The final release entry must be cut from this section only after the complete
release matrix in `PLAN.md` passes on AMD64 and ARM64.
