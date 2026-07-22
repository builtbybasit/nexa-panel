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
PANEL_HOSTNAME=""
TLS_EMAIL=""
ALLOW_INSECURE=0

usage() {
  cat <<'EOF'
Usage: install.sh [options]

  With no --panel-hostname the installer runs the quick-start: it publishes the
  panel on :8888 over plain HTTP (reachable by the server's public IP), auto-
  creates the first administrator and prints its password, and opens UFW ports
  22, 80, 443, and 8888. Pass --panel-hostname with --tls-email for a domain
  fronted by a Let's Encrypt certificate instead.

  --binary PATH   Install PATH as /usr/bin/nexa. If omitted, an existing
                  /usr/bin/nexa is left alone (the test image bind-mounts one).
  --no-start      Configure and enable the services but do not start them.
                  Implied when systemd is not running, e.g. in an image build.
  --panel-hostname HOST
                  Publish the panel through Nginx for this DNS hostname.
                  Without it, Nginx exposes a local bootstrap listener only.
  --tls-email EMAIL
                  Obtain and renew a Let's Encrypt certificate for --panel-hostname.
  --allow-insecure-http
                  Permit publishing --panel-hostname over plaintext HTTP without
                  TLS. Unsafe: authentication crosses the network in cleartext.
                  Only for deployments that terminate TLS in front of this node.
  -h, --help      Show this message.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BINARY="${2:-}"; [[ -n "$BINARY" ]] || { echo "error: --binary needs a path" >&2; exit 2; }; shift 2 ;;
    --no-start) START=0; shift ;;
    --panel-hostname) PANEL_HOSTNAME="${2:-}"; [[ -n "$PANEL_HOSTNAME" ]] || { echo "error: --panel-hostname needs a host" >&2; exit 2; }; shift 2 ;;
    --tls-email) TLS_EMAIL="${2:-}"; [[ -n "$TLS_EMAIL" ]] || { echo "error: --tls-email needs an address" >&2; exit 2; }; shift 2 ;;
    --allow-insecure-http) ALLOW_INSECURE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

log()  { echo "==> $*"; }
warn() { echo "warning: $*" >&2; }
die()  { echo "error: $*" >&2; exit 1; }

valid_hostname() {
  local hostname="$1" label
  local -a labels
  (( ${#hostname} <= 253 )) || return 1
  [[ "$hostname" != .* && "$hostname" != *. && "$hostname" != *..* ]] || return 1
  IFS='.' read -r -a labels <<< "$hostname"
  (( ${#labels[@]} > 0 )) || return 1
  for label in "${labels[@]}"; do
    (( ${#label} >= 1 && ${#label} <= 63 )) || return 1
    [[ "$label" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || return 1
  done
}

# --- host checks ------------------------------------------------------------
[[ "${EUID:-$(id -u)}" -eq 0 ]] || die "this installer must run as root (try: sudo ./scripts/install.sh ...)"
[[ -r /etc/os-release ]] || die "cannot identify this host: /etc/os-release is missing"
# shellcheck disable=SC1091
. /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || die "unsupported distribution '${ID:-unknown}': Nexa Panel targets Ubuntu (the PHP repository it manages publishes for Ubuntu only)"
[[ "${VERSION_ID:-}" == "24.04" ]] || die "unsupported Ubuntu release '${VERSION_ID:-unknown}': Nexa Panel currently supports Ubuntu 24.04 LTS only"
[[ -n "${VERSION_CODENAME:-}" ]] || die "this Ubuntu release reports no VERSION_CODENAME, which the package repositories need"
if [[ -n "$PANEL_HOSTNAME" ]] && ! valid_hostname "$PANEL_HOSTNAME"; then
  die "--panel-hostname must be a valid DNS hostname"
fi
[[ -z "$TLS_EMAIL" || -n "$PANEL_HOSTNAME" ]] || die "--tls-email requires --panel-hostname"
# Publishing exposes the login and session cookie to the network. A named host
# must therefore use TLS (--tls-email) unless the operator explicitly accepts
# cleartext (--allow-insecure-http, e.g. TLS terminates on a load balancer in
# front of this node). The default no-hostname mode is the public-IP quick-start
# below: the panel is published on :8888 over plain HTTP by design.
if [[ -n "$PANEL_HOSTNAME" && -z "$TLS_EMAIL" && "$ALLOW_INSECURE" -eq 0 ]]; then
  die "refusing to publish $PANEL_HOSTNAME over plaintext HTTP: pass --tls-email EMAIL to provision TLS, or --allow-insecure-http to accept cleartext authentication (only safe when TLS terminates in front of this node)"
fi

# Whether the API must accept non-loopback plaintext HTTP. True for the default
# public-IP quick-start (no hostname; panel on :8888) and the explicit
# load-balancer opt-in; false only when a named host is fronted by TLS here.
if [[ -z "$PANEL_HOSTNAME" || "$ALLOW_INSECURE" -eq 1 ]]; then
  INSECURE_HTTP=1
else
  INSECURE_HTTP=0
fi

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
  nginx cron certbot python3-certbot-nginx \
  logrotate \
  postgresql-common libjson-perl \
  passwd util-linux \
  ufw \
  rclone \
  openssh-server \
  podman fuse-overlayfs \
  ca-certificates curl gnupg software-properties-common

# pgAdmin deploys as a Podman Quadlet unit: a `.container` file the systemd
# generator turns into a `.service` on reload. phpMyAdmin is installed natively
# on demand and shares the node's Nginx/PHP-FPM stack.
# That generator ships with Podman >= 4.4, while the generated definitions also
# use PodmanArgs support available from 4.5. Without either, deploying pgAdmin
# fails with a bare "Unit not found"; warn now so the cause is obvious.
# The rest of the panel does not depend on it, so this is a warning, not a hard
# failure.
PODMAN_VERSION="$(podman --version 2>/dev/null | awk '{print $3}' || true)"
if [[ ! -x /usr/lib/systemd/system-generators/podman-system-generator ]] ||
   [[ -z "$PODMAN_VERSION" ]] || ! dpkg --compare-versions "$PODMAN_VERSION" ge 4.5; then
  warn "Podman 4.5+ with Quadlet is required for the pgAdmin database web client (found ${PODMAN_VERSION:-unknown}). Ubuntu 24.04 ships a compatible version."
fi

# Podman storage driver. On a normal host the kernel's native overlay driver
# works. When the node itself runs inside a container whose backing filesystem
# is overlayfs (the CI test image, some nested setups), the kernel refuses
# overlay-on-overlay and Podman needs the fuse-overlayfs mount program instead —
# without it every `podman run` fails with "'overlay' is not supported over
# overlayfs". Enable fuse-overlayfs only when native overlay is unavailable, so
# real hosts keep the faster in-kernel driver.
if ! podman info >/dev/null 2>&1 && podman info 2>&1 | grep -q "not supported over overlayfs"; then
  log "Native overlay unavailable (overlayfs backing filesystem); enabling fuse-overlayfs for Podman"
  install -d -m 0755 /etc/containers
  cat > /etc/containers/storage.conf <<'EOF'
[storage]
driver = "overlay"

[storage.options.overlay]
mount_program = "/usr/bin/fuse-overlayfs"
EOF
fi

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
  log "Validating and installing $BINARY as /usr/bin/nexa"
  install -m 0755 "$BINARY" /usr/bin/nexa.new
  if ! /usr/bin/nexa.new version >/dev/null 2>&1; then
    rm -f /usr/bin/nexa.new
    die "$BINARY is not an executable Nexa Panel binary for this node"
  fi
  mv -f /usr/bin/nexa.new /usr/bin/nexa
elif [[ ! -x /usr/bin/nexa ]]; then
  [[ "$START" -eq 0 || ! -d /run/systemd/system ]] || die "no Nexa Panel binary to start; pass --binary PATH"
  warn "no nexa binary at /usr/bin/nexa — install one before starting the enabled services"
fi

# --- packaged units and configuration ---------------------------------------
log "Installing the packaged units, service account, and directories"
install -d -m 0755 /usr/lib/systemd/system /usr/lib/sysusers.d /usr/lib/tmpfiles.d
install -m 0644 "$ROOT_DIR/packaging/systemd/nexa-agent.service" /usr/lib/systemd/system/nexa-agent.service
install -m 0644 "$ROOT_DIR/packaging/systemd/nexa-api.service" /usr/lib/systemd/system/nexa-api.service
install -m 0644 "$ROOT_DIR/packaging/systemd/nexa-panel-system-backup.service" /usr/lib/systemd/system/nexa-panel-system-backup.service
install -m 0644 "$ROOT_DIR/packaging/systemd/nexa-panel-system-backup.timer" /usr/lib/systemd/system/nexa-panel-system-backup.timer
install -m 0644 "$ROOT_DIR/packaging/sysusers/nexa-panel.conf" /usr/lib/sysusers.d/nexa-panel.conf
install -m 0644 "$ROOT_DIR/packaging/tmpfiles/nexa-panel.conf" /usr/lib/tmpfiles.d/nexa-panel.conf

# API service drop-in. Two production settings the shipped unit deliberately does
# not hardcode: the first-run bootstrap token path (owner-only, under the state
# dir, not the /tmp dev default), and — only when serving non-loopback plaintext
# HTTP — the NEXA_ALLOW_INSECURE_HTTP opt-in the secure-transport guard requires.
# Written every run so a later re-install without cleartext drops the override.
NEXA_API_DROPIN_DIR="/etc/systemd/system/nexa-api.service.d"
NEXA_API_DROPIN="$NEXA_API_DROPIN_DIR/10-nexa-panel.conf"
install -d -m 0755 "$NEXA_API_DROPIN_DIR"
# Retire the older single-purpose drop-in name so its setting cannot linger.
rm -f "$NEXA_API_DROPIN_DIR/10-insecure-http.conf"
{
  echo "[Service]"
  echo "Environment=NEXA_BOOTSTRAP_TOKEN=/var/lib/nexa-panel/bootstrap.token"
  if [[ "$INSECURE_HTTP" -eq 1 ]]; then
    echo "Environment=NEXA_ALLOW_INSECURE_HTTP=1"
  fi
} > "$NEXA_API_DROPIN"
chmod 0644 "$NEXA_API_DROPIN"
if [[ "$INSECURE_HTTP" -eq 1 ]]; then
  warn "publishing over plaintext HTTP: authentication and the session cookie cross the network in cleartext; put TLS in front for anything beyond a test node"
fi

# Create the account and the managed tree now rather than waiting for the next
# boot, so the services can start at the end of this script.
systemd-sysusers
systemd-tmpfiles --create

# Nginx is the only process allowed to reach the control-plane socket. Site PHP
# accounts are not members of the nexa group, so they cannot bypass proxy
# authentication or forge trusted forwarding headers.
usermod -a -G nexa www-data

log "Configuring the panel reverse proxy"
install -d -m 0755 /etc/nginx/sites-available /etc/nginx/sites-enabled
if [[ -n "$PANEL_HOSTNAME" ]]; then
  PANEL_LISTEN="80"
  PANEL_SERVER_NAME="$PANEL_HOSTNAME"
else
  # Quick-start default: reachable on every interface by the server's public IP,
  # no domain or certificate. server_name "_" is the catch-all so any Host (the
  # bare IP included) matches. /metrics stays loopback-gated inside the template.
  PANEL_LISTEN="8888"
  PANEL_SERVER_NAME="_"
fi
sed -e "s/__LISTEN__/$PANEL_LISTEN/g" -e "s/__SERVER_NAME__/$PANEL_SERVER_NAME/g" \
  "$ROOT_DIR/packaging/nginx/nexa-panel.conf.template" > /etc/nginx/sites-available/nexa-panel.conf
ln -sfn /etc/nginx/sites-available/nexa-panel.conf /etc/nginx/sites-enabled/nexa-panel.conf
nginx -t

# --- per-site SFTP -----------------------------------------------------------
# Optional per-site SFTP access is served by the node's own OpenSSH: the SFTP
# operator drops one `Match User` block per enabled site into
# /etc/ssh/sshd_config.d/. That directory only takes effect if the main config
# Includes it. Debian and Ubuntu ship the Include line by default; add it (once)
# when a hardened or older config lacks it, so enabling SFTP from the panel works
# without hand-editing sshd_config. Password auth stays disabled globally — each
# site's Match block re-enables it for that one jailed account only.
log "Ensuring OpenSSH honours the per-site SFTP drop-in directory"
install -d -m 0755 /etc/ssh/sshd_config.d
if ! grep -qsE '^\s*Include\s+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config; then
  printf '\n# Added by Nexa Panel: honour per-site SFTP drop-ins.\nInclude /etc/ssh/sshd_config.d/*.conf\n' >> /etc/ssh/sshd_config
fi
# Generate any missing host keys so `sshd -t` can load them; in an image build
# the package postinst may not have run ssh-keygen. Idempotent: -A only creates
# keys that are absent.
ssh-keygen -A
# `sshd -t` needs the privilege-separation directory to exist. On a real host
# tmpfiles/ssh.service create it at boot, but during an image build /run is empty
# and the check would abort with "Missing privilege separation directory".
install -d -m 0755 /run/sshd
# Validate before enabling so a pre-existing broken config surfaces here, not on
# the first site that enables SFTP.
sshd -t

# --- services ---------------------------------------------------------------
log "Enabling services"
systemctl enable nexa-agent.service nexa-api.service nginx.service cron.service ssh.service nexa-panel-system-backup.timer

# systemd is not running inside an image build, so there is nothing to start and
# `systemctl start` would fail; the units are enabled and start on first boot.
if [[ "$START" -eq 0 ]]; then
  log "Configured. Not starting services (--no-start)."
elif [[ ! -d /run/systemd/system ]]; then
  log "Configured. systemd is not running here, so the services will start on first boot."
else
  log "Starting services"
  systemctl daemon-reload
  systemctl restart nexa-agent.service nexa-api.service nginx.service
  systemctl start nexa-panel-system-backup.timer
  if [[ -n "$TLS_EMAIL" ]]; then
    log "Obtaining a TLS certificate for $PANEL_HOSTNAME"
    certbot --nginx --non-interactive --agree-tos --redirect --email "$TLS_EMAIL" -d "$PANEL_HOSTNAME"
  fi

  # Firewall: allow SSH first so enabling UFW can never lock out this session,
  # then the web and panel ports, then turn the firewall on.
  if command -v ufw >/dev/null 2>&1; then
    log "Opening the firewall (UFW): 22, 80, 443, 8888"
    ufw allow 22/tcp
    ufw allow 80/tcp
    ufw allow 443/tcp
    ufw allow 8888/tcp
    ufw --force enable >/dev/null || warn "could not enable UFW; open 22/80/443/8888 manually"
  fi

  # Auto-create the first administrator and print its credentials, closing the
  # bootstrap window immediately. The seed helper is shared with the disposable
  # Docker node's boot unit so the two never drift; it only creates the account
  # in a plaintext mode, where the API socket call is not blocked by the TLS
  # guard. A named host fronted by TLS keeps the create-first-account-in-browser
  # flow instead.
  panel_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [[ -n "$panel_ip" ]] || panel_ip="<server-ip>"
  if [[ -n "$PANEL_HOSTNAME" ]]; then
    panel_url="http${TLS_EMAIL:+s}://${PANEL_HOSTNAME}/"
  else
    panel_url="http://${panel_ip}:8888/"
  fi
  if [[ "$INSECURE_HTTP" -eq 1 ]]; then
    bash "$ROOT_DIR/scripts/nexa-seed-admin.sh" "$panel_url"
  else
    log "Nexa Panel is available at ${panel_url}"
  fi
fi
