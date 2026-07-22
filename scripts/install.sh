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
# Three ways to run it, all the same script:
#
#   # from a source checkout
#   sudo ./scripts/install.sh --binary dist/nexa-linux-amd64
#
#   # from an unpacked release tarball (the binary is found in bin/nexa)
#   sudo ./nexa-panel-1.2.3-linux-amd64/scripts/install.sh
#
#   # remote bootstrap: fetch, verify and unpack the release, then install it
#   sudo ./scripts/install.sh --download --github-token-file /root/gh.token
#
# Nothing below resolves a repository root: the packaging tree and the seed
# helper are read out of the directory this script lives in, which is what makes
# the checkout and the tarball interchangeable.
#
set -euo pipefail

# systemctl pipes into a pager when it thinks it has a terminal, and a pager in a
# non-interactive install does not return — it waits for a keypress nobody is
# there to give. A fixed PATH keeps a root account with an unusual environment
# from resolving one of the tools below to something unexpected.
export SYSTEMD_PAGER=''
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_FILE=""
LOG_TAIL_LINES=40
VERBOSE="${NEXA_INSTALL_VERBOSE:-0}"
BINARY=""
START=1
MODE="install"
PANEL_HOSTNAME=""
TLS_EMAIL=""
ALLOW_INSECURE=0
ALLOW_EXISTING=0
SKIP_PREFLIGHT="${NEXA_SKIP_PREFLIGHT:-0}"
DOWNLOAD=0
RELEASE_REPO="${NEXA_RELEASE_REPO:-builtbybasit/nexa-panel}"
RELEASE_VERSION=""
GITHUB_TOKEN_FILE=""
SAVE_TOKEN=1
RELEASE_TOKEN_PATH="/etc/nexa-panel/release.token"
PHP_VERSION="8.3"
INSTALL_PHP=1
PHP_EXPLICIT=0
INSTALL_COMPOSER=1
COMPOSER_EXPLICIT=0
# Set whenever a managed file on this node actually changed. --sync-packaging
# restarts only when it did, so a refresh on a live node is a no-op rather than
# a service blip.
CHANGED=0

usage() {
  cat <<'EOF'
Usage: install.sh [options]

  With no --panel-hostname the installer runs the quick-start: it publishes the
  panel on :8888 over plain HTTP (reachable by the server's public IP), auto-
  creates the first administrator and prints its password, and opens UFW ports
  22, 80, 443, and 8888. Pass --panel-hostname with --tls-email for a domain
  fronted by a Let's Encrypt certificate instead. Both paths end with an
  administrator account you can sign in with.

  --binary PATH   Install PATH as /usr/bin/nexa. Defaults to bin/nexa when run
                  from an unpacked release tarball. If omitted with no bundled
                  binary, an existing /usr/bin/nexa is left alone (the test
                  image bind-mounts one).
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
  --allow-existing
                  Upgrade a node whose panel is currently running. Without it
                  the preflight refuses to install over a live install.
  --skip-preflight
                  Do not run `nexa doctor --preflight` before changing the
                  machine. Also settable as NEXA_SKIP_PREFLIGHT=1.
  --php-version X.Y
                  PHP branch to install for site hosting (default 8.3). Further
                  versions are installed on demand from Applications.
  --no-php        Do not install any PHP. Sites cannot be created until a PHP
                  version is installed from Applications.
  --no-composer   Do not install Composer. Deployments that need it install it
                  on demand instead.
  --verbose       Write every command's output to the console instead of to a
                  transcript file. Also settable as NEXA_INSTALL_VERBOSE=1.

 Remote bootstrap:
  --download      Fetch the release tarball for this host's architecture from
                  GitHub, verify its sha256 sidecar, unpack it, and run the
                  installer out of it. Every other option is passed through.
  --release-version vX.Y.Z
                  Release tag to download. Defaults to the latest release.
  --repo OWNER/NAME
                  Release repository (default builtbybasit/nexa-panel). Also
                  settable as NEXA_RELEASE_REPO.
  --github-token-file PATH
                  Read the GitHub token from PATH. Also settable as
                  NEXA_GITHUB_TOKEN. Optional: without it the same API is used
                  anonymously, which only works for a public repository.
  --no-save-token
                  Do not persist the token to /etc/nexa-panel/release.token.
                  `nexa self-update` reads that file to fetch later releases,
                  so without it every upgrade needs the token supplied again.

 Maintenance:
  --sync-packaging
                  Refresh only the packaging side — prerequisites, sysusers,
                  tmpfiles, systemd units, the Nginx template — and nothing
                  else. The database, the seeded administrator, and how the
                  panel is published (hostname, TLS, port) are left untouched.
                  This is what `nexa self-update` runs after swapping the binary.
  -h, --help      Show this message.
EOF
}

# Arguments the remote bootstrap hands to the installer it unpacks. Everything
# except the download options themselves, which have already been honoured by
# the time the unpacked copy runs.
PASSTHROUGH=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BINARY="${2:-}"; [[ -n "$BINARY" ]] || { echo "error: --binary needs a path" >&2; exit 2; }; PASSTHROUGH+=("$1" "$2"); shift 2 ;;
    --no-start) START=0; PASSTHROUGH+=("$1"); shift ;;
    --panel-hostname) PANEL_HOSTNAME="${2:-}"; [[ -n "$PANEL_HOSTNAME" ]] || { echo "error: --panel-hostname needs a host" >&2; exit 2; }; PASSTHROUGH+=("$1" "$2"); shift 2 ;;
    --tls-email) TLS_EMAIL="${2:-}"; [[ -n "$TLS_EMAIL" ]] || { echo "error: --tls-email needs an address" >&2; exit 2; }; PASSTHROUGH+=("$1" "$2"); shift 2 ;;
    --allow-insecure-http) ALLOW_INSECURE=1; PASSTHROUGH+=("$1"); shift ;;
    --allow-existing) ALLOW_EXISTING=1; PASSTHROUGH+=("$1"); shift ;;
    --skip-preflight) SKIP_PREFLIGHT=1; PASSTHROUGH+=("$1"); shift ;;
    --php-version) PHP_VERSION="${2:-}"; [[ -n "$PHP_VERSION" ]] || { echo "error: --php-version needs a version such as 8.3" >&2; exit 2; }; PHP_EXPLICIT=1; PASSTHROUGH+=("$1" "$2"); shift 2 ;;
    --no-php) INSTALL_PHP=0; PHP_EXPLICIT=1; PASSTHROUGH+=("$1"); shift ;;
    --no-composer) INSTALL_COMPOSER=0; COMPOSER_EXPLICIT=1; PASSTHROUGH+=("$1"); shift ;;
    --verbose) VERBOSE=1; PASSTHROUGH+=("$1"); shift ;;
    --download) DOWNLOAD=1; shift ;;
    --release-version) RELEASE_VERSION="${2:-}"; [[ -n "$RELEASE_VERSION" ]] || { echo "error: --release-version needs a tag" >&2; exit 2; }; shift 2 ;;
    --repo) RELEASE_REPO="${2:-}"; [[ -n "$RELEASE_REPO" ]] || { echo "error: --repo needs OWNER/NAME" >&2; exit 2; }; shift 2 ;;
    --github-token-file) GITHUB_TOKEN_FILE="${2:-}"; [[ -n "$GITHUB_TOKEN_FILE" ]] || { echo "error: --github-token-file needs a path" >&2; exit 2; }; PASSTHROUGH+=("$1" "$2"); shift 2 ;;
    --no-save-token) SAVE_TOKEN=0; PASSTHROUGH+=("$1"); shift ;;
    --sync-packaging) MODE="sync-packaging"; PASSTHROUGH+=("$1"); shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# Console and transcript are separate streams. fd 3 is always the real console;
# once start_transcript() runs, stdout and stderr belong to the log file, so
# every apt, dpkg and systemctl line lands there instead of scrolling past an
# operator who cannot act on it. emit() writes a line to both.
exec 3>&1

emit() {
  printf '%s\n' "$*" >&3
  # When no transcript is open, stdout still IS fd 3 and this would double the line.
  [[ -n "$LOG_FILE" ]] && printf '%s\n' "$*" || true
}

log()  { emit "==> $*"; }
warn() { emit "warning: $*"; }
die()  { emit "error: $*"; exit 1; }

# on_failure runs for any non-zero exit. The log file is the point of the split,
# but it is not always reachable — inside a Docker build layer or a CI runner it
# disappears with the container — so the tail is replayed to the console rather
# than merely pointed at.
on_failure() {
  local status=$?
  trap - ERR EXIT
  if [[ "$status" -eq 0 ]]; then
    [[ -n "$LOG_FILE" ]] && printf '\nTranscript: %s\n' "$LOG_FILE" >&3
    exit 0
  fi
  if [[ -n "$LOG_FILE" ]]; then
    {
      printf '\n'
      printf 'The install failed (exit %s). Last %s lines of the transcript:\n\n' "$status" "$LOG_TAIL_LINES"
      tail -n "$LOG_TAIL_LINES" "$LOG_FILE" 2>/dev/null || true
      printf '\nThe full transcript is at %s\n' "$LOG_FILE"
    } >&3
  fi
  exit "$status"
}

# start_transcript opens the log and hands stdout/stderr to it. mktemp rather than
# a fixed name: a predictable path in a world-writable directory is a symlink
# target on a shared host, and this file is written as root. 0600 because the
# transcript records the node's configuration.
start_transcript() {
  [[ "$VERBOSE" -eq 1 ]] && return 0
  LOG_FILE="$(mktemp /var/log/nexa-panel-install.XXXXXXXX.log 2>/dev/null ||
    mktemp "${TMPDIR:-/tmp}/nexa-panel-install.XXXXXXXX.log")" || return 0
  chmod 0600 "$LOG_FILE"
  exec >>"$LOG_FILE" 2>&1
  trap on_failure ERR EXIT
}

# The heredoc delimiter is quoted deliberately: the logo is drawn in slashes and
# backslashes, and an unquoted delimiter would let the shell eat every one of
# them. Printed only for an interactive install — a build layer, a --sync-packaging
# upgrade, or a piped log gets no banner.
show_logo() {
  # fd 3, not stdout: once the transcript is open stdout is a file, and the logo
  # belongs on the operator's terminal or nowhere.
  [[ -t 3 ]] || return 0
  cat >&3 <<"NEXA_LOGO"

    _   _________  __ ___       ____  ___    _   __________
   / | / / ____/ |/ //   |     / __ \/   |  / | / / ____/ /
  /  |/ / __/  |   // /| |    / /_/ / /| | /  |/ / __/ / /
 / /|  / /___ /   |/ ___ |   / ____/ ___ |/ /|  / /___/ /___
/_/ |_/_____//_/|_/_/  |_|  /_/   /_/  |_/_/ |_/_____/_____/

NEXA_LOGO
}

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

# install_managed SOURCE MODE DESTINATION — place a managed file only when its
# content differs, and record that this node changed. Comparing rather than
# copying unconditionally is what lets --sync-packaging decide whether a restart
# is warranted at all.
install_managed() {
  local source="$1" mode="$2" destination="$3"
  if [[ -f "$destination" ]] && cmp -s "$source" "$destination"; then
    return 0
  fi
  install -m "$mode" "$source" "$destination"
  CHANGED=1
}

# The local address the host would send traffic out of. Deliberately not
# `hostname -I`: that lists every address in interface order, so any node running
# Docker or libvirt answers with a bridge address the operator cannot reach. The
# routing table knows which address egress actually leaves by; the scope-global
# scan is the fallback for a host with no default route.
detect_local_ip() {
  local ip
  ip="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}')"
  if [[ -z "$ip" ]]; then
    ip="$(ip -o -4 addr show scope global 2>/dev/null |
      awk '$2 !~ /^(docker|br-|veth|virbr|tun|tap)/ { split($4, a, "/"); print a[1]; exit }')"
  fi
  printf '%s' "$ip"
}

# The address to print in the ready banner. On most cloud providers every local
# address is private and NAT'd, and the operator cannot open any of them, so a
# public reflector gets asked first; the local answer is what a node with no
# egress — or a provider that assigns the public address directly — falls back to.
detect_panel_ip() {
  local ip
  ip="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  if [[ ! "$ip" =~ ^[0-9]+(\.[0-9]+){3}$ ]]; then
    ip="$(detect_local_ip)"
  fi
  [[ -n "$ip" ]] || ip="<server-ip>"
  printf '%s' "$ip"
}

# render_panel_vhost LISTEN SERVER_NAME DESTINATION — expand the packaged Nginx
# template. The template declares one IPv4 listener; a node with an AAAA record
# has to answer on IPv6 as well, so a matching [::] listener is added here when
# the template does not already carry one — and never when it does, because
# nginx rejects a duplicate listen directive outright.
render_panel_vhost() {
  local listen="$1" server_name="$2" destination="$3" template
  template="$PACKAGING_DIR/nginx/nexa-panel.conf.template"
  if grep -q 'listen \[::\]' "$template"; then
    sed -e "s/__LISTEN__/$listen/g" -e "s/__SERVER_NAME__/$server_name/g" "$template" > "$destination"
    return 0
  fi
  sed -e "s/^\( *\)listen __LISTEN__;/\1listen __LISTEN__;\n\1listen [::]:__LISTEN__;/" \
      -e "s/__LISTEN__/$listen/g" \
      -e "s/__SERVER_NAME__/$server_name/g" \
      "$template" > "$destination"
}

# --- host checks ------------------------------------------------------------
[[ "${EUID:-$(id -u)}" -eq 0 ]] || die "this installer must run as root (try: sudo ./scripts/install.sh ...)"
[[ -r /etc/os-release ]] || die "cannot identify this host: /etc/os-release is missing"
# shellcheck disable=SC1091
. /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || die "unsupported distribution '${ID:-unknown}': Nexa Panel targets Ubuntu (the PHP repository it manages publishes for Ubuntu only)"
[[ "${VERSION_ID:-}" == "24.04" ]] || die "unsupported Ubuntu release '${VERSION_ID:-unknown}': Nexa Panel currently supports Ubuntu 24.04 LTS only"
[[ -n "${VERSION_CODENAME:-}" ]] || die "this Ubuntu release reports no VERSION_CODENAME, which the package repositories need"

# Architecture is checked here rather than at the first apt failure: every
# repository this installer adds publishes amd64 and arm64 only, and a node that
# gets all the way to installing PHP before finding that out has already had its
# sources.list.d rewritten.
HOST_ARCH="$(dpkg --print-architecture)"
case "$HOST_ARCH" in
  amd64|arm64) ;;
  *) die "unsupported architecture '$HOST_ARCH': Nexa Panel publishes amd64 and arm64 builds only" ;;
esac

# The whole node model is systemd units. A container or a minimal image without
# systemd-sysv will accept every step below and then fail at `systemctl enable`,
# leaving a half-configured host.
[[ -d /run/systemd/system ]] ||
  warn "systemd does not appear to be running (no /run/systemd/system); unit installation will be configured but cannot start"

# Upstream publishes fewer database series for arm64 than for amd64 — MySQL in
# particular ships no arm64 packages for its newer series — so an arm64 node sees
# a shorter list in Applications. Said once here rather than discovered later as
# an unexplained install failure.
if [[ "$HOST_ARCH" == "arm64" && "$MODE" != "sync-packaging" ]]; then
  warn "arm64 node: upstream publishes no arm64 packages for some MySQL series, so Applications will offer fewer database versions than on amd64"
fi
if [[ -n "$PANEL_HOSTNAME" ]] && ! valid_hostname "$PANEL_HOSTNAME"; then
  die "--panel-hostname must be a valid DNS hostname"
fi
[[ -z "$TLS_EMAIL" || -n "$PANEL_HOSTNAME" ]] || die "--tls-email requires --panel-hostname"
[[ "$INSTALL_PHP" -eq 0 || "$PHP_VERSION" =~ ^[0-9]+\.[0-9]+$ ]] || die "--php-version must be a PHP branch such as 8.3"
# --sync-packaging refreshes an install; it deliberately owns no publishing
# decision, so the flags that make one are rejected rather than quietly ignored.
if [[ "$MODE" == "sync-packaging" && ( -n "$PANEL_HOSTNAME" || -n "$TLS_EMAIL" ) ]]; then
  die "--sync-packaging refreshes the packaging only and never changes how the panel is published; re-run without --panel-hostname/--tls-email"
fi
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

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

# --- release credential -----------------------------------------------------
# Optional by design. Without a token the same GitHub API URLs are used
# anonymously, so nothing here has to change if the repository is ever made
# public. The token is only ever held in a variable, handed to curl on stdin
# (never on a command line, where the process table would leak it), and written
# to a 0600 root-owned file.
GITHUB_TOKEN="${NEXA_GITHUB_TOKEN:-}"
GITHUB_TOKEN_SOURCE="NEXA_GITHUB_TOKEN"
if [[ -n "$GITHUB_TOKEN_FILE" ]]; then
  [[ -r "$GITHUB_TOKEN_FILE" ]] || die "cannot read the GitHub token file $GITHUB_TOKEN_FILE"
  GITHUB_TOKEN="$(tr -d '[:space:]' < "$GITHUB_TOKEN_FILE")"
  GITHUB_TOKEN_SOURCE="$GITHUB_TOKEN_FILE"
  [[ -n "$GITHUB_TOKEN" ]] || die "the GitHub token file $GITHUB_TOKEN_FILE is empty"
fi

# --- remote bootstrap -------------------------------------------------------
# github_fetch URL ACCEPT OUTPUT — retrieve one GitHub API URL, printing the
# HTTP status. The credential goes in through curl's config file on stdin, so it
# never reaches the process table; a printf builtin writes it, so it never
# reaches an argument vector either. curl drops the Authorization header on a
# cross-host redirect, which is what lets the asset URL follow through to the
# signed storage location without the storage backend rejecting the request.
github_fetch() {
  local url="$1" accept="$2" output="$3" code
  code="$(
    {
      printf 'url = "%s"\n' "$url"
      printf 'header = "Accept: %s"\n' "$accept"
      printf 'header = "X-GitHub-Api-Version: 2022-11-28"\n'
      printf 'user-agent = "nexa-panel-installer"\n'
      if [[ -n "$GITHUB_TOKEN" ]]; then
        printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN"
      fi
    } | curl --silent --show-error --location --retry 2 --max-time 900 \
             --output "$output" --write-out '%{http_code}' --config - || true
  )"
  printf '%s' "${code:-000}"
}

# release_http_check CODE WHAT — turn a GitHub status into an answer an operator
# can act on. A private repository answers 404 to an anonymous caller, which
# reads exactly like a missing release; saying so is the difference between a
# one-minute fix and a wasted afternoon.
release_http_check() {
  local code="$1" what="$2"
  case "$code" in
    200) return 0 ;;
    401|403)
      die "GitHub rejected the credential while fetching $what (HTTP $code): the token from $GITHUB_TOKEN_SOURCE is invalid, expired, or has no read access to $RELEASE_REPO" ;;
    404)
      if [[ -z "$GITHUB_TOKEN" ]]; then
        die "GitHub returned 404 for $what and no token was supplied. $RELEASE_REPO is a private repository, whose release assets cannot be downloaded anonymously — this is almost certainly a missing credential, not a missing release. Pass --github-token-file PATH (or set NEXA_GITHUB_TOKEN) with a token that can read $RELEASE_REPO."
      fi
      die "GitHub returned 404 for $what: ${RELEASE_VERSION:-the latest release} does not exist in $RELEASE_REPO, or the token from $GITHUB_TOKEN_SOURCE cannot see it" ;;
    000)
      die "could not reach api.github.com to fetch $what: check this host's outbound network and DNS" ;;
    *)
      die "GitHub returned HTTP $code while fetching $what" ;;
  esac
}

# release_asset_url METADATA NAME — the API download URL of one release asset.
# A private repository does not serve browser_download_url, so assets are
# fetched from /repos/OWNER/NAME/releases/assets/<id>, which is the asset
# object's own "url" field. Splitting the document at every "{" puts each asset
# object on its own line, which is enough to read the pair out without adding a
# JSON parser to the installer's dependencies.
release_asset_url() {
  local metadata="$1" name="$2"
  tr '{' '\n' < "$metadata" \
    | grep -F "\"name\":\"$name\"" \
    | sed -n 's|.*"url":"\(https://[^"]*/releases/assets/[0-9]*\)".*|\1|p' \
    | head -n 1
}

# Fetch, verify and unpack the release for this host, then install out of the
# unpacked copy. Nothing from the network is executed — or written anywhere but
# the temporary directory — until the sha256 sidecar matches.
bootstrap_from_release() {
  local arch tarball checksum endpoint metadata code tarball_url checksum_url
  local unpacked installer expected actual status
  command -v curl >/dev/null 2>&1 || die "the remote bootstrap needs curl; install it with: apt-get install -y curl"
  command -v tar  >/dev/null 2>&1 || die "the remote bootstrap needs tar"
  arch="$(dpkg --print-architecture)"
  case "$arch" in
    amd64|arm64) ;;
    *) die "no Nexa Panel release is published for this architecture ($arch)" ;;
  esac
  tarball="nexa-panel-linux-${arch}.tar.gz"
  checksum="${tarball}.sha256"
  if [[ -n "$RELEASE_VERSION" ]]; then
    endpoint="https://api.github.com/repos/${RELEASE_REPO}/releases/tags/${RELEASE_VERSION}"
  else
    endpoint="https://api.github.com/repos/${RELEASE_REPO}/releases/latest"
  fi

  log "Resolving ${RELEASE_VERSION:-the latest release} of $RELEASE_REPO for $arch"
  if [[ -z "$GITHUB_TOKEN" ]]; then
    warn "no GitHub token supplied; the release API will be called anonymously, which only works for a public repository"
  fi
  metadata="$WORK_DIR/release.json"
  code="$(github_fetch "$endpoint" "application/vnd.github+json" "$metadata")"
  release_http_check "$code" "the release metadata"
  tarball_url="$(release_asset_url "$metadata" "$tarball")"
  [[ -n "$tarball_url" ]] || die "${RELEASE_VERSION:-the latest release} of $RELEASE_REPO publishes no $tarball"
  checksum_url="$(release_asset_url "$metadata" "$checksum")"
  [[ -n "$checksum_url" ]] || die "${RELEASE_VERSION:-the latest release} of $RELEASE_REPO publishes no $checksum, so the download cannot be verified"

  log "Downloading $tarball"
  code="$(github_fetch "$tarball_url" "application/octet-stream" "$WORK_DIR/$tarball")"
  release_http_check "$code" "$tarball"
  code="$(github_fetch "$checksum_url" "application/octet-stream" "$WORK_DIR/$checksum")"
  release_http_check "$code" "$checksum"

  expected="$(awk 'NR==1{print $1}' "$WORK_DIR/$checksum")"
  [[ -n "$expected" ]] || die "$checksum carries no checksum"
  actual="$(sha256sum "$WORK_DIR/$tarball" | awk '{print $1}')"
  [[ "$expected" == "$actual" ]] || die "checksum mismatch for $tarball: expected $expected, got $actual. The download was truncated or tampered with; nothing has been installed."
  log "Verified $tarball (sha256 $actual)"

  unpacked="$WORK_DIR/unpacked"
  # Inside WORK_DIR, which mktemp already created 0700.
  mkdir -p "$unpacked"
  tar -xzf "$WORK_DIR/$tarball" -C "$unpacked"
  installer="$(find "$unpacked" -mindepth 2 -maxdepth 3 -type f -path '*/scripts/install.sh' | head -n 1)"
  [[ -n "$installer" ]] || die "$tarball does not contain scripts/install.sh; it is not a Nexa Panel release bundle"

  log "Installing from the unpacked release"
  status=0
  bash "$installer" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"} || status=$?
  exit "$status"
}

if [[ "$DOWNLOAD" -eq 1 ]]; then
  bootstrap_from_release
fi

# --- layout -----------------------------------------------------------------
# Everything the installer reads is resolved relative to this script, never to a
# repository root: that is the whole reason a source checkout, an unpacked
# release tarball, and the copy the remote bootstrap unpacks are interchangeable.
# A release bundle also carries the binary, so `--binary` is optional there.
PACKAGING_DIR="$ROOT_DIR/packaging"
SEED_SCRIPT="$ROOT_DIR/scripts/nexa-seed-admin.sh"
[[ -f "$PACKAGING_DIR/systemd/nexa-api.service" ]] || die "no packaging tree at $PACKAGING_DIR: run this script from a source checkout or an unpacked release tarball, or pass --download to fetch one"
RELEASE_LABEL=""
if [[ -f "$ROOT_DIR/RELEASE" ]]; then
  RELEASE_LABEL="$(sed -n 's/^version=//p' "$ROOT_DIR/RELEASE" | head -n 1)"
  if [[ -z "$BINARY" && -f "$ROOT_DIR/bin/nexa" ]]; then
    BINARY="$ROOT_DIR/bin/nexa"
  fi
fi

export DEBIAN_FRONTEND=noninteractive
start_transcript
if [[ "$MODE" == "sync-packaging" ]]; then
  log "Refreshing Nexa Panel packaging${RELEASE_LABEL:+ ($RELEASE_LABEL)} on Ubuntu ${VERSION_ID:-?} (${VERSION_CODENAME}), $(dpkg --print-architecture)"
else
  show_logo
  log "Installing Nexa Panel${RELEASE_LABEL:+ $RELEASE_LABEL} on Ubuntu ${VERSION_ID:-?} (${VERSION_CODENAME}), $(dpkg --print-architecture)"
fi

# --- binary -----------------------------------------------------------------
# Installed before anything else is touched so the preflight below can run: the
# checks live in the binary, and everything after them mutates the machine.
# Placing /usr/bin/nexa is the one change that has to happen first, and it is
# also the most reversible.
if [[ -n "$BINARY" ]]; then
  [[ -f "$BINARY" ]] || die "no binary at $BINARY"
  log "Validating and installing $BINARY as /usr/bin/nexa"
  install -m 0755 "$BINARY" /usr/bin/nexa.new
  if ! /usr/bin/nexa.new version >/dev/null 2>&1; then
    rm -f /usr/bin/nexa.new
    die "$BINARY is not an executable Nexa Panel binary for this node"
  fi
  # A binary compiled without `-tags embed` runs, reports a version, and serves
  # no web UI at all — the SPA route is simply absent, so the panel answers every
  # browser request with a 404 and looks broken for reasons nothing explains.
  # The embedded asset tree is addressed as dist/index.html inside the binary, so
  # its absence is a reliable, cheap proof that the frontend was left out.
  if ! LC_ALL=C grep -qa 'dist/index.html' /usr/bin/nexa.new; then
    rm -f /usr/bin/nexa.new
    die "$BINARY carries no embedded web UI, so the panel would serve no interface: build releases with scripts/build-linux-release.sh (which compiles with -tags embed), not a plain \`go build\`"
  fi
  mv -f /usr/bin/nexa.new /usr/bin/nexa
  CHANGED=1
elif [[ ! -x /usr/bin/nexa ]]; then
  [[ "$START" -eq 0 || ! -d /run/systemd/system ]] || die "no Nexa Panel binary to start; pass --binary PATH"
  warn "no nexa binary at /usr/bin/nexa — install one before starting the enabled services"
fi

# --- preflight --------------------------------------------------------------
# Last chance to abort cleanly: from the next section on, this script adds
# repositories, installs packages and rewrites system configuration, none of
# which is undone by aborting half way. `nexa doctor --preflight` reports the
# conflicts a shell check cannot see — bound ports and their owners, a live
# panel, another control panel, free space, reachable package sources — and
# exits non-zero when any of them is a blocker.
#
# A packaging refresh runs on a node that is by definition already installed and
# serving, which every one of those checks would report as a conflict, so it
# skips them.
if [[ "$MODE" == "sync-packaging" ]]; then
  :
elif [[ "$SKIP_PREFLIGHT" == "1" ]]; then
  warn "skipping the preflight checks (--skip-preflight); conflicts on this host will surface as install failures instead"
elif [[ ! -x /usr/bin/nexa ]]; then
  warn "no nexa binary yet, so the preflight checks cannot run; pass --binary PATH to have them checked"
else
  log "Running preflight checks"
  PREFLIGHT_ARGS=(doctor --preflight)
  if [[ "$ALLOW_EXISTING" -eq 1 ]]; then
    PREFLIGHT_ARGS+=(--allow-existing)
  fi
  if ! /usr/bin/nexa "${PREFLIGHT_ARGS[@]}"; then
    die "preflight found blockers; resolve them and re-run, or pass --skip-preflight to install anyway"
  fi
fi

# --- prerequisites ----------------------------------------------------------
# The set the operators shell out to; see packaging/REQUIREMENTS.md. postgresql-common
# also creates the `postgres` account that the tmpfiles tree below is owned by,
# so it has to land before systemd-tmpfiles runs.
# Composer deliberately stays out of this block: it runs before ppa:ondrej/php is
# added, and Ubuntu's `composer` pulls the distro `php-cli`, which would make the
# Applications catalog read that PHP branch as installed forever. It is installed
# further down instead, after the PHP branch this node will host sites with.
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
  git unzip rsync acl sudo \
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
# Both steps are skipped once their source list exists: they are idempotent but
# not cheap, and a re-run (or a --sync-packaging refresh on a live node) should
# not pay for work already done.
#
# MySQL/MariaDB are deliberately NOT configured here: each series is a separate
# pinned repository (MariaDB in the URL path, MySQL in the apt component), so
# there is no single repository to add — the operator adds exactly the one series
# being installed. See internal/platform/operators/packages/databases.go.
REPOSITORIES_ADDED=0
if ! grep -rqs 'ondrej' /etc/apt/sources.list.d/ 2>/dev/null; then
  log "Configuring the PHP repository (ppa:ondrej/php)"
  add-apt-repository -y ppa:ondrej/php
  REPOSITORIES_ADDED=1
fi

if ! grep -rqs 'apt.postgresql.org' /etc/apt/sources.list.d/ 2>/dev/null; then
  log "Configuring the PostgreSQL repository (PGDG)"
  /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
  REPOSITORIES_ADDED=1
fi

# Leave a populated index behind: the catalog reads it with apt-cache and never
# refreshes it itself, so a node whose lists were cleaned would silently offer a
# truncated catalog until the next install.
if [[ "$REPOSITORIES_ADDED" -eq 1 ]]; then
  log "Refreshing the package index"
  apt-get update -qq
fi

# --- PHP runtime ------------------------------------------------------------
# A node with no PHP cannot host a site at all: the runtime catalog enumerates
# /etc/php, which does not exist until a php*-fpm package is installed, and the
# panel disables site creation when the node reports no runtime. Installing one
# branch — not every branch the ondrej repository offers — is what makes a fresh
# node usable; further versions are installed on demand from Applications.
# --sync-packaging never introduces PHP on its own: an operator who declined it,
# or who manages PHP elsewhere, must not have it appear during an upgrade.
if [[ "$MODE" == "sync-packaging" && "$PHP_EXPLICIT" -eq 0 ]]; then
  INSTALL_PHP=0
fi
if [[ "$INSTALL_PHP" -eq 1 ]]; then
  # FPM and CLI are the runtime; the extension set is the one a stock PHP
  # application (WordPress, Laravel) refuses to install without, kept short
  # deliberately — anything beyond it is a per-version choice made from the PHP
  # page rather than an install-time guess.
  log "Installing PHP $PHP_VERSION (FPM, CLI, and the common extension set)"
  apt-get install -y --no-install-recommends \
    "php${PHP_VERSION}-fpm" "php${PHP_VERSION}-cli" \
    "php${PHP_VERSION}-mysql" "php${PHP_VERSION}-pgsql" \
    "php${PHP_VERSION}-mbstring" "php${PHP_VERSION}-xml" \
    "php${PHP_VERSION}-curl" "php${PHP_VERSION}-zip" \
    "php${PHP_VERSION}-gd" "php${PHP_VERSION}-intl" "php${PHP_VERSION}-bcmath"
  systemctl enable "php${PHP_VERSION}-fpm.service" >/dev/null 2>&1 ||
    warn "could not enable php${PHP_VERSION}-fpm.service; enable it before creating a site"
elif [[ "$MODE" != "sync-packaging" ]]; then
  warn "no PHP installed (--no-php): site creation stays disabled until a PHP version is installed from Applications"
fi

# --- Composer ---------------------------------------------------------------
# Composer lands after the PHP block, never before it: the `composer` package
# depends on a php-cli provider, so apt satisfies that dependency with whatever
# CLI is already installed. With the ondrej CLI in place it binds to that branch;
# on a node with no PHP at all apt would pull the distro php-cli instead, and the
# Applications catalog would then read that branch as installed forever. This is
# the same ordering the deployment prepare job follows for a site's own tooling
# (internal/platform/operators/deploy/prepare.go).
# --sync-packaging never introduces Composer on its own, for the same reason it
# never introduces PHP: an upgrade must not add tooling the operator declined.
if [[ "$MODE" == "sync-packaging" && "$COMPOSER_EXPLICIT" -eq 0 ]]; then
  INSTALL_COMPOSER=0
fi
if [[ "$INSTALL_COMPOSER" -eq 1 ]] && ! command -v php >/dev/null 2>&1; then
  warn "no php-cli on this node; skipping Composer so apt cannot pull the distro PHP branch in as a dependency"
  INSTALL_COMPOSER=0
fi
if [[ "$INSTALL_COMPOSER" -eq 1 ]]; then
  log "Installing Composer"
  apt-get install -y --no-install-recommends composer
elif [[ "$MODE" != "sync-packaging" && "$COMPOSER_EXPLICIT" -eq 1 ]]; then
  warn "no Composer installed (--no-composer): deployments that need it install it on demand"
fi

# --- packaged units and configuration ---------------------------------------
log "Installing the packaged units, service account, and directories"
install -d -m 0755 /usr/lib/systemd/system /usr/lib/sysusers.d /usr/lib/tmpfiles.d
install_managed "$PACKAGING_DIR/systemd/nexa-agent.service" 0644 /usr/lib/systemd/system/nexa-agent.service
install_managed "$PACKAGING_DIR/systemd/nexa-api.service" 0644 /usr/lib/systemd/system/nexa-api.service
install_managed "$PACKAGING_DIR/systemd/nexa-panel-system-backup.service" 0644 /usr/lib/systemd/system/nexa-panel-system-backup.service
install_managed "$PACKAGING_DIR/systemd/nexa-panel-system-backup.timer" 0644 /usr/lib/systemd/system/nexa-panel-system-backup.timer
install_managed "$PACKAGING_DIR/sysusers/nexa-panel.conf" 0644 /usr/lib/sysusers.d/nexa-panel.conf
install_managed "$PACKAGING_DIR/tmpfiles/nexa-panel.conf" 0644 /usr/lib/tmpfiles.d/nexa-panel.conf

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
# The drop-in records how this node was published. A packaging refresh must not
# rewrite that from flags it was not given, so an existing one is left exactly as
# the install wrote it.
if [[ "$MODE" == "sync-packaging" && -f "$NEXA_API_DROPIN" ]]; then
  log "Leaving the existing API service drop-in unchanged"
else
  {
    echo "[Service]"
    echo "Environment=NEXA_BOOTSTRAP_TOKEN=/var/lib/nexa-panel/bootstrap.token"
    if [[ "$INSECURE_HTTP" -eq 1 && "$MODE" != "sync-packaging" ]]; then
      echo "Environment=NEXA_ALLOW_INSECURE_HTTP=1"
    fi
  } > "$WORK_DIR/nexa-api.dropin"
  install_managed "$WORK_DIR/nexa-api.dropin" 0644 "$NEXA_API_DROPIN"
fi
if [[ "$INSECURE_HTTP" -eq 1 && "$MODE" != "sync-packaging" ]]; then
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

# Hand the release credential to the node. `nexa self-update` fetches later
# releases from the same private repository and reads exactly this path, so an
# operator who supplied a token once never has to supply it again. Root-only:
# it is a repository credential, and the API account has no business reading it.
if [[ -n "$GITHUB_TOKEN" && "$SAVE_TOKEN" -eq 1 ]]; then
  log "Storing the release credential in $RELEASE_TOKEN_PATH"
  (umask 077; printf '%s\n' "$GITHUB_TOKEN" > "$RELEASE_TOKEN_PATH")
  chown root:root "$RELEASE_TOKEN_PATH"
  chmod 0600 "$RELEASE_TOKEN_PATH"
fi

log "Configuring the panel reverse proxy"
install -d -m 0755 /etc/nginx/sites-available /etc/nginx/sites-enabled
PANEL_VHOST="/etc/nginx/sites-available/nexa-panel.conf"
if [[ "$MODE" == "sync-packaging" ]]; then
  # Refreshing the vhost must not change how the panel is published, so the
  # listener and server name are read back out of the file the install wrote
  # rather than re-derived from flags this run was never given. A vhost certbot
  # has rewritten is left completely alone: it carries the TLS listener,
  # certificate paths and redirect that only certbot knows how to reproduce, and
  # re-rendering the template over it would take the panel off HTTPS.
  if [[ ! -f "$PANEL_VHOST" ]]; then
    warn "no panel vhost at $PANEL_VHOST; run the installer without --sync-packaging to publish the panel"
  elif grep -q 'managed by Certbot' "$PANEL_VHOST"; then
    log "Leaving the certbot-managed panel vhost unchanged"
  else
    PANEL_LISTEN="$(sed -n 's/^[[:space:]]*listen[[:space:]]\{1,\}\([0-9]\{1,\}\);.*/\1/p' "$PANEL_VHOST" | head -n 1)"
    PANEL_SERVER_NAME="$(sed -n 's/^[[:space:]]*server_name[[:space:]]\{1,\}\([^;]*\);.*/\1/p' "$PANEL_VHOST" | head -n 1)"
    if [[ -z "$PANEL_LISTEN" || -z "$PANEL_SERVER_NAME" ]]; then
      warn "could not read the listener out of $PANEL_VHOST; leaving it unchanged"
    else
      render_panel_vhost "$PANEL_LISTEN" "$PANEL_SERVER_NAME" "$WORK_DIR/nexa-panel.conf"
      install_managed "$WORK_DIR/nexa-panel.conf" 0644 "$PANEL_VHOST"
    fi
  fi
else
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
  render_panel_vhost "$PANEL_LISTEN" "$PANEL_SERVER_NAME" "$WORK_DIR/nexa-panel.conf"
  install_managed "$WORK_DIR/nexa-panel.conf" 0644 "$PANEL_VHOST"
fi
# Only ever link a vhost that exists: a --sync-packaging run on a node the panel
# was never published on has nothing to link, and a dangling symlink in
# sites-enabled fails `nginx -t` outright, taking every other site down with it.
if [[ -f "$PANEL_VHOST" ]]; then
  ln -sfn "$PANEL_VHOST" /etc/nginx/sites-enabled/nexa-panel.conf
fi

# Ubuntu's nginx package enables a stock `default` site that answers on :80 with
# the "Welcome to nginx" page. Left enabled, the node's bare address advertises
# the web server to anyone who probes it, and a panel-managed site that wants to
# be the default vhost cannot become one. Panel-managed sites bring their own
# vhosts, so retire it.
if [[ -e /etc/nginx/sites-enabled/default ]]; then
  log "Disabling the stock Nginx default site"
  rm -f /etc/nginx/sites-enabled/default
  CHANGED=1
fi
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

# A packaging refresh stops here: it owns the packaging and nothing else — no
# database, no administrator, no firewall, no certificate — and restarts only
# what a real change requires, so it is safe to run repeatedly on a live node.
if [[ "$MODE" == "sync-packaging" ]]; then
  if [[ "$START" -eq 0 ]]; then
    log "Packaging refreshed. Not restarting services (--no-start)."
  elif [[ ! -d /run/systemd/system ]]; then
    log "Packaging refreshed. systemd is not running here, so the services pick it up on first boot."
  elif [[ "$CHANGED" -eq 1 ]]; then
    log "Packaging changed; reloading systemd and restarting the panel services"
    systemctl daemon-reload
    systemctl restart nexa-agent.service nexa-api.service
    systemctl reload nginx.service || systemctl restart nginx.service
  else
    log "Packaging is already up to date; nothing to restart."
  fi
  exit 0
fi

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
  # then the web ports, then turn the firewall on. 8888 is opened only for the
  # quick-start, which publishes the panel there; a named host is served on
  # 80/443, where opening 8888 would widen the exposed surface for nothing.
  if command -v ufw >/dev/null 2>&1; then
    UFW_PORTS=(22/tcp 80/tcp 443/tcp)
    if [[ -z "$PANEL_HOSTNAME" ]]; then
      UFW_PORTS+=(8888/tcp)
    fi
    log "Opening the firewall (UFW): ${UFW_PORTS[*]}"
    for ufw_port in "${UFW_PORTS[@]}"; do
      ufw allow "$ufw_port"
    done
    ufw --force enable >/dev/null || warn "could not enable UFW; open ${UFW_PORTS[*]} manually"
  fi

  # Auto-create the first administrator and print its credentials, closing the
  # bootstrap window immediately. The seed helper is shared with the disposable
  # Docker node's boot unit so the two never drift.
  #
  # It runs on the TLS path too, and has to: the bootstrap endpoint refuses any
  # non-loopback caller that presents no bootstrap token, and the SPA has no
  # field to put one in, so a remote browser opening a published hostname gets
  # 403 bootstrap_forbidden and can never complete first-run setup. Seeding here
  # over the node's own API socket is what makes every documented install path
  # end with an account the operator can actually sign in with.
  panel_ip="$(detect_panel_ip)"
  if [[ -n "$PANEL_HOSTNAME" ]]; then
    panel_url="http${TLS_EMAIL:+s}://${PANEL_HOSTNAME}/"
  else
    panel_url="http://${panel_ip}:8888/"
  fi
  if [[ -f "$SEED_SCRIPT" ]]; then
    # Straight to the console, never through the transcript: the helper prints the
    # generated administrator password, and a password written into a log file
    # outlives the install and every reason anyone had to read it.
    bash "$SEED_SCRIPT" "$panel_url" >&3 2>&3
  else
    warn "no seed helper at $SEED_SCRIPT; create the first administrator with the bootstrap token in /var/lib/nexa-panel/bootstrap.token"
    log "Nexa Panel is available at ${panel_url}"
  fi
fi
