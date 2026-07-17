#!/usr/bin/env bash
# Nexa Panel node installer.
#
# Prepares an Ubuntu host to run the panel: host prerequisites, the package
# repositories the Applications catalog reads from, the packaged systemd units,
# the service account, the managed directory tree, and the services themselves.
#
# This is the single source of truth for node layout. The test image runs this
# same script rather than reproducing its steps, so what CI exercises is what a
# real host gets — a Dockerfile that reimplements the install drifts from it
# silently, and the drift only shows up as a bug on someone's server.
#
# Everything here is idempotent: re-running upgrades an existing node in place.
#
#   sudo ./scripts/install.sh --binary dist/nexa-linux-amd64
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BINARY=""
START=1

usage() {
  cat <<'EOF'
Usage: install.sh [options]

  --binary PATH   Install PATH as /usr/bin/nexa. If omitted, an existing
                  /usr/bin/nexa is left alone (the test image bind-mounts one).
  --no-start      Configure and enable the services but do not start them.
                  Implied when systemd is not running, e.g. in an image build.
  -h, --help      Show this message.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BINARY="${2:-}"; [[ -n "$BINARY" ]] || { echo "error: --binary needs a path" >&2; exit 2; }; shift 2 ;;
    --no-start) START=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

log()  { echo "==> $*"; }
warn() { echo "warning: $*" >&2; }
die()  { echo "error: $*" >&2; exit 1; }

# --- host checks ------------------------------------------------------------
[[ "${EUID:-$(id -u)}" -eq 0 ]] || die "this installer must run as root (try: sudo $0 $*)"
[[ -r /etc/os-release ]] || die "cannot identify this host: /etc/os-release is missing"
# shellcheck disable=SC1091
. /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || die "unsupported distribution '${ID:-unknown}': Nexa Panel targets Ubuntu (the PHP repository it manages publishes for Ubuntu only)"
[[ -n "${VERSION_CODENAME:-}" ]] || die "this Ubuntu release reports no VERSION_CODENAME, which the package repositories need"

export DEBIAN_FRONTEND=noninteractive
log "Installing Nexa Panel on Ubuntu ${VERSION_ID:-?} (${VERSION_CODENAME}), $(dpkg --print-architecture)"

# --- prerequisites ----------------------------------------------------------
# The set the operators shell out to; see packaging/REQUIREMENTS.md. postgresql-common
# also creates the `postgres` account that the tmpfiles tree below is owned by,
# so it has to land before systemd-tmpfiles runs.
log "Refreshing the package index"
apt-get update -qq

log "Installing host prerequisites"
apt-get install -y --no-install-recommends \
  systemd systemd-sysv dbus \
  nginx cron certbot \
  postgresql-common libjson-perl \
  passwd util-linux \
  ca-certificates curl gnupg software-properties-common

# --- package repositories ---------------------------------------------------
# The Applications catalog lists what this node's repositories actually offer,
# rather than a table baked into the binary, so a PHP or PostgreSQL release
# published after this build becomes installable without a code change. That only
# works if the repositories are here: with Ubuntu's archive alone the catalog can
# offer exactly one PHP (8.3 on noble) and one PostgreSQL, and there is no way to
# reach any other version, because the panel only adds a repository as a side
# effect of installing from it. Configuring them at install time is what makes the
# catalog mean anything.
#
# MySQL/MariaDB are deliberately NOT configured here: each series is a separate
# pinned repository (MariaDB in the URL path, MySQL in the apt component), so
# there is no single repository to add — the operator adds exactly the one series
# being installed. See internal/platform/operators/packages/databases.go.
log "Configuring the PHP repository (ppa:ondrej/php)"
add-apt-repository -y ppa:ondrej/php

log "Configuring the PostgreSQL repository (PGDG)"
/usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y

# Leave a populated index behind: the catalog reads it with apt-cache and never
# refreshes it itself, so a node whose lists were cleaned would silently offer a
# truncated catalog until the next install.
log "Refreshing the package index"
apt-get update -qq

# --- binary -----------------------------------------------------------------
if [[ -n "$BINARY" ]]; then
  [[ -f "$BINARY" ]] || die "no binary at $BINARY"
  log "Installing $BINARY as /usr/bin/nexa"
  install -m 0755 "$BINARY" /usr/bin/nexa
elif [[ ! -x /usr/bin/nexa ]]; then
  warn "no nexa binary at /usr/bin/nexa — pass --binary PATH, or put one there before starting the services"
fi

# --- packaged units and configuration ---------------------------------------
log "Installing the packaged units, service account, and directories"
install -d -m 0755 /usr/lib/systemd/system /usr/lib/sysusers.d /usr/lib/tmpfiles.d
install -m 0644 "$ROOT_DIR/packaging/systemd/nexa-agent.service" /usr/lib/systemd/system/nexa-agent.service
install -m 0644 "$ROOT_DIR/packaging/systemd/nexa-api.service" /usr/lib/systemd/system/nexa-api.service
install -m 0644 "$ROOT_DIR/packaging/sysusers/nexa-panel.conf" /usr/lib/sysusers.d/nexa-panel.conf
install -m 0644 "$ROOT_DIR/packaging/tmpfiles/nexa-panel.conf" /usr/lib/tmpfiles.d/nexa-panel.conf

# Create the account and the managed tree now rather than waiting for the next
# boot, so the services can start at the end of this script.
systemd-sysusers
systemd-tmpfiles --create

# --- services ---------------------------------------------------------------
log "Enabling services"
systemctl enable nexa-agent.service nexa-api.service nginx.service cron.service

# systemd is not running inside an image build, so there is nothing to start and
# `systemctl start` would fail; the units are enabled and start on first boot.
if [[ "$START" -eq 0 ]]; then
  log "Configured. Not starting services (--no-start)."
elif [[ ! -d /run/systemd/system ]]; then
  log "Configured. systemd is not running here, so the services will start on first boot."
else
  log "Starting services"
  systemctl daemon-reload
  systemctl restart nexa-agent.service nexa-api.service
  log "Nexa Panel is running. The API listens on 127.0.0.1:8080."
fi
