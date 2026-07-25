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

- Self-update no longer reports a false failure when the activation restarts the
  control panel: the cancellation that restart raises is now surfaced as such, so
  the interrupted job is left running and recovered to the committed outcome
  instead of being recorded as failed on a node that updated correctly.
- Metrics forwarding through packaged Nginx.
- Site deletion ownership of schedules and backup references.
- pgAdmin proxy path/session behavior and destructive UI safety checks.

The final release entry must be cut from this section only after the complete
release matrix in `PLAN.md` passes on AMD64 and ARM64.

## v0.5.13 — 2026-07-24

### Fixed

- Uninstall no longer aborts on nodes with the native phpMyAdmin integration:
  the PHP-FPM session drop-in now carries the Nexa Panel ownership header the
  uninstaller requires before deleting it. Found testing uninstall on a live
  node.
- Purge-mode uninstall dry-run prints its complete plan again: it no longer
  trips over the panel's own header-less vhosts (`nexa-panel.conf`, the
  phpMyAdmin gateway), which are removed by exact path rather than by glob.
- Purge-mode uninstall no longer fails at `userdel` when a managed site's
  PHP-FPM workers or scheduled tasks are still running: site-owned processes are
  terminated before the account is deleted.
- Uninstall clears lingering systemd failed-state for every panel unit (not just
  the two core services), so a unit that was failed beforehand no longer survives
  as a phantom in `systemctl --failed` after removal.

## v0.5.12 — 2026-07-24

### Added

- A site's PHP version can be changed from its settings. The new PHP-FPM pool is
  provisioned and the outgoing pool is retired only once the change is applied,
  with the old runtime restored on rollback.

## v0.5.11 — 2026-07-24

### Added

- Site creation can set an SFTP password up front. It is stored only as a hash
  until the site is activated, then applied automatically as the site's SFTP
  access; a staging failure is reported in the activation log without failing
  the activation and is retried on the next one.

## v0.5.10 — 2026-07-24

### Changed

- Site creation is now a guided step wizard: the monolithic form is split into
  a stepper flow with dedicated template, configuration, and success stages.

### Added

- A shared password generator that mixes upper, lower, digit, and symbol classes
  from a cryptographically secure source, wired through the password field.
