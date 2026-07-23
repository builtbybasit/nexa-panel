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
DRY_RUN=0
VERIFY_INGRESS_ONLY=0
PANEL_HOSTNAME=""
TLS_EMAIL=""
ALLOW_INSECURE=0
INGRESS_EXPLICIT=0
MANAGE_FIREWALL=0
TLS_ACTIVE=0
ALLOW_EXISTING=0
ADOPT_EXISTING=0
SKIP_PREFLIGHT="${NEXA_SKIP_PREFLIGHT:-0}"
DOWNLOAD=0
RELEASE_REPO="${NEXA_RELEASE_REPO:-builtbybasit/nexa-panel}"
RELEASE_VERSION=""
GITHUB_TOKEN_FILE=""
SAVE_TOKEN=1
RELEASE_TOKEN_PATH="/etc/nexa-panel/release.token"
LIFECYCLE_LOCK_PATH="/run/lock/nexa-panel-lifecycle.lock"
RELEASE_HELPER="$ROOT_DIR/scripts/nexa-release-helper.py"
RELEASE_SIGNERS="$ROOT_DIR/packaging/release-signers"
PHP_VERSION="8.3"
INSTALL_PHP=1
PHP_EXPLICIT=0
INSTALL_COMPOSER=1
COMPOSER_EXPLICIT=0
# Set whenever a managed file on this node actually changed. --sync-packaging
# restarts only when it did, so a refresh on a live node is a no-op rather than
# a service blip.
CHANGED=0
# How long the published listener is given to answer before the install is
# declared failed. Nginx is already running by then; this covers a slow first
# request and, on a TLS install, Certbot's own reload.
INGRESS_ATTEMPTS="${NEXA_INGRESS_ATTEMPTS:-30}"
INGRESS_DELAY="${NEXA_INGRESS_DELAY:-2}"
# Removing a supplementary group from www-data does not change already-running
# Nginx workers. When that legacy privilege is repaired, a graceful reload is
# insufficient because the old workers may retain the privileged `nexa` gid
# until their connections drain. Force a full stop/start in that case.
NGINX_HARD_RESTART=0

usage() {
  cat <<'EOF'
Usage: install.sh [options]

  The secure default binds the panel to loopback on :8888 for access through an
  SSH tunnel. Production publishing requires --panel-hostname with --tls-email.
  Plaintext network publishing is available only with the explicit
  --allow-insecure-http test/load-balancer opt-in. The installer never enables
  or rewrites a host firewall unless --manage-firewall is supplied.

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
                  Publish over plaintext HTTP without TLS. With no hostname this
                  exposes :8888 on all interfaces. This is strictly for a
                  disposable test node; external TLS termination is not a
                  supported substitute for the installer's end-to-end TLS mode.
  --manage-firewall
                  Add only the panel's required rules when UFW is already active.
                  Never enables UFW, changes its defaults, or guesses an SSH port.
  --allow-existing
                  Upgrade a node whose panel is currently running. Without it
                  the preflight refuses to install over a live install.
  --adopt-existing
                  One-time migration for a pre-v1 Nexa install that has the
                  exact service account, binary, units, state DB, and token
                  layout but no ownership marker. Ambiguous paths are refused.
  --dry-run       Print the complete plan — every managed path, unit, package
                  repository, firewall rule and service action — and change
                  nothing. Does not require root.
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
  --verify-ingress
                  Fetch the panel through the listener the node publishes it on
                  and exit nonzero if it does not answer. Read-only; this is the
                  same check the installer runs before reporting success.
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
    --panel-hostname) PANEL_HOSTNAME="${2:-}"; [[ -n "$PANEL_HOSTNAME" ]] || { echo "error: --panel-hostname needs a host" >&2; exit 2; }; INGRESS_EXPLICIT=1; PASSTHROUGH+=("$1" "$2"); shift 2 ;;
    --tls-email) TLS_EMAIL="${2:-}"; [[ -n "$TLS_EMAIL" ]] || { echo "error: --tls-email needs an address" >&2; exit 2; }; INGRESS_EXPLICIT=1; PASSTHROUGH+=("$1" "$2"); shift 2 ;;
    --allow-insecure-http) ALLOW_INSECURE=1; INGRESS_EXPLICIT=1; PASSTHROUGH+=("$1"); shift ;;
    --manage-firewall) MANAGE_FIREWALL=1; PASSTHROUGH+=("$1"); shift ;;
    --allow-existing) ALLOW_EXISTING=1; PASSTHROUGH+=("$1"); shift ;;
    --adopt-existing) ADOPT_EXISTING=1; PASSTHROUGH+=("$1"); shift ;;
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
    --dry-run) DRY_RUN=1; PASSTHROUGH+=("$1"); shift ;;
    --sync-packaging) MODE="sync-packaging"; PASSTHROUGH+=("$1"); shift ;;
    --verify-ingress) VERIFY_INGRESS_ONLY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -n "$TLS_EMAIL" ]]; then
  TLS_ACTIVE=1
fi

# Console and transcript are separate streams. fd 8 is always the real console;
# once start_transcript() runs, stdout and stderr belong to the log file, so
# every apt, dpkg and systemctl line lands there instead of scrolling past an
# operator who cannot act on it. emit() writes a line to both.
exec 8>&1

emit() {
  printf '%s\n' "$*" >&8
  # When no transcript is open, stdout still IS fd 8 and this would double the line.
  [[ -n "$LOG_FILE" ]] && printf '%s\n' "$*" || true
}

log()  { emit "==> $*"; }
warn() { emit "warning: $*"; }
die()  { emit "error: $*"; exit 1; }

if [[ "$MODE" == "sync-packaging" && -n "$BINARY" ]]; then
  die "--sync-packaging never swaps the executable; omit --binary and use 'nexa self-update' for a transactional binary update"
fi

# --- rollback journal -------------------------------------------------------
# Every host mutation is recorded — before it happens — as one record in an
# append-only journal, and a failure replays that journal backwards. Without it
# a failure after the package repositories, the sshd_config Include, the units,
# the vhost or the firewall rules leaves a half-mutated host that somebody has to
# reason about by hand.
#
# Records are appended for absences too ("this path did not exist"), because
# "remove what we created" is as much a part of the undo as "put back what we
# replaced". The journal and the saved originals live in WORK_DIR: the rollback
# runs before cleanup, and a rollback that could not finish keeps the directory.
#
# Fields are separated by an ASCII unit separator rather than a tab, so `read`
# preserves empty fields and no path can smuggle a separator in.
ROLLBACK_SEPARATOR=$'\x1f'
ROLLBACK_JOURNAL=""
ROLLBACK_BACKUPS=""
ROLLBACK_SEQUENCE=0
ROLLBACK_FAILURES=0
ROLLBACK_IN_PROGRESS=0
ROLLBACK_RESTART_UNITS=()

journal_start() {
  ROLLBACK_BACKUPS="$WORK_DIR/rollback"
  install -d -m 0700 "$ROLLBACK_BACKUPS"
  ROLLBACK_JOURNAL="$WORK_DIR/rollback.journal"
  : > "$ROLLBACK_JOURNAL"
  chmod 0600 "$ROLLBACK_JOURNAL"
}

journal() {
  local field record=""
  [[ -n "$ROLLBACK_JOURNAL" ]] || return 0
  for field in "$@"; do
    record+="${field//$ROLLBACK_SEPARATOR/ }$ROLLBACK_SEPARATOR"
  done
  printf '%s\n' "$record" >> "$ROLLBACK_JOURNAL"
}

# journal_path PATH — record what PATH is right now, before it is written,
# replaced, linked over or removed. A regular file is copied aside with its mode
# and ownership; a symlink only needs its target; anything else is left alone.
journal_path() {
  local path="$1" slot
  [[ -n "$ROLLBACK_JOURNAL" ]] || return 0
  if [[ -L "$path" ]]; then
    journal symlink "$path" "$(readlink "$path")"
  elif [[ -f "$path" ]]; then
    ROLLBACK_SEQUENCE=$((ROLLBACK_SEQUENCE + 1))
    slot="$ROLLBACK_BACKUPS/$ROLLBACK_SEQUENCE"
    cp -p -- "$path" "$slot"
    journal file "$path" "$slot" "$(stat -c '%a' "$path")" "$(stat -c '%U:%G' "$path")"
  elif [[ -e "$path" ]]; then
    journal untouched "$path"
  else
    journal created "$path"
  fi
}

# ensure_directory MODE PATH [OWNER GROUP] — `install -d`, journalled. The undo
# is an rmdir, so a directory that turns out to hold anything else survives.
ensure_directory() {
  local mode="$1" path="$2" owner="${3:-root}" group="${4:-root}"
  [[ -e "$path" || -L "$path" ]] || journal directory "$path"
  install -d -m "$mode" -o "$owner" -g "$group" "$path"
}

journal_unit_enablement() {
  local unit
  for unit in "$@"; do
    journal unit "$unit" "$(systemctl show --property=UnitFileState --value "$unit" 2>/dev/null || true)"
  done
}

journal_service_state() {
  local unit active
  for unit in "$@"; do
    active=0
    systemctl is-active --quiet "$unit" 2>/dev/null && active=1
    journal service "$unit" "$active"
  done
}

rollback_step() {
  local description="$1"
  shift
  emit "$description"
  if ! "$@"; then
    ROLLBACK_FAILURES=$((ROLLBACK_FAILURES + 1))
    warn "rollback step failed: $*"
  fi
}

rollback_queue_restart() {
  local unit="$1" queued
  for queued in ${ROLLBACK_RESTART_UNITS[@]+"${ROLLBACK_RESTART_UNITS[@]}"}; do
    [[ "$queued" == "$unit" ]] && return 0
  done
  ROLLBACK_RESTART_UNITS+=("$unit")
}

# Reload and revalidate what the restored files belong to. Validation failures
# are reported rather than fatal: the operator needs the whole picture, and a
# second exit from inside the failure path would hide it.
rollback_finalize() {
  local unit
  if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    rollback_step "RUN systemctl daemon-reload" systemctl daemon-reload
    for unit in ${ROLLBACK_RESTART_UNITS[@]+"${ROLLBACK_RESTART_UNITS[@]}"}; do
      rollback_step "RUN systemctl restart $unit" systemctl restart "$unit"
    done
  fi
  if command -v sshd >/dev/null 2>&1; then
    if sshd -t >/dev/null 2>&1; then
      if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet ssh.service 2>/dev/null; then
        rollback_step "RUN systemctl reload ssh.service" systemctl reload ssh.service
      fi
    else
      ROLLBACK_FAILURES=$((ROLLBACK_FAILURES + 1))
      warn "OpenSSH configuration does not validate after rollback; inspect /etc/ssh/sshd_config before restarting sshd"
    fi
  fi
  if command -v nginx >/dev/null 2>&1; then
    if nginx -t >/dev/null 2>&1; then
      if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx.service 2>/dev/null; then
        rollback_step "RUN systemctl reload nginx.service" systemctl reload nginx.service
      fi
    else
      ROLLBACK_FAILURES=$((ROLLBACK_FAILURES + 1))
      warn "Nginx configuration does not validate after rollback; run 'nginx -t' and inspect /etc/nginx/sites-enabled before reloading"
    fi
  fi
}

# Replay the journal newest-first. Every step is guarded: this runs on a host
# that is by definition already in an unexpected state, so a step whose target
# has moved is reported and skipped, never allowed to abort the rest of the undo.
perform_rollback() {
  local action path slot mode owner target state active port root roots existed
  local retain_apt_sources=0 reported_packages=0
  local -a fields created_roots
  [[ -n "$ROLLBACK_JOURNAL" && -s "$ROLLBACK_JOURNAL" ]] || return 0
  ROLLBACK_IN_PROGRESS=1
  emit ""
  emit "Rolling back this run's changes, newest first."
  while IFS="$ROLLBACK_SEPARATOR" read -r -a fields; do
    action="${fields[0]:-}"
    case "$action" in
      file)
        path="${fields[1]}"; slot="${fields[2]}"; mode="${fields[3]}"; owner="${fields[4]}"
        if [[ ! -f "$slot" ]]; then
          ROLLBACK_FAILURES=$((ROLLBACK_FAILURES + 1))
          warn "no saved original for $path"
          continue
        fi
        rollback_step "RESTORE $path" install -m "$mode" -o "${owner%%:*}" -g "${owner##*:}" "$slot" "$path"
        ;;
      symlink)
        path="${fields[1]}"; target="${fields[2]}"
        rollback_step "RESTORE $path -> $target" ln -sfn -- "$target" "$path"
        ;;
      created)
        path="${fields[1]}"
        [[ -e "$path" || -L "$path" ]] || continue
        rollback_step "REMOVE $path" rm -f -- "$path"
        ;;
      apt_source)
        path="${fields[1]}"
        if [[ "$retain_apt_sources" -eq 1 ]]; then
          emit "RETAIN $path (packages were already installed from it)"
          continue
        fi
        [[ -e "$path" || -L "$path" ]] || continue
        rollback_step "REMOVE $path" rm -f -- "$path"
        ;;
      packages)
        # Reverse order matters here: a repository is only retained when the
        # record for packages installed *from* it has already been replayed.
        [[ "${fields[1]}" == "prerequisites" ]] || retain_apt_sources=1
        if [[ "$reported_packages" -eq 0 ]]; then
          reported_packages=1
          emit "RETAIN the Ubuntu packages installed by this run (a dpkg transaction cannot be reversed here)"
        fi
        ;;
      directory)
        path="${fields[1]}"
        [[ -d "$path" && ! -L "$path" ]] || continue
        if rmdir -- "$path" 2>/dev/null; then
          emit "REMOVE $path"
        else
          emit "RETAIN $path (not empty)"
        fi
        ;;
      unit)
        path="${fields[1]}"; state="${fields[2]}"
        case "$state" in
          enabled|enabled-runtime|linked|linked-runtime|alias) continue ;;
        esac
        command -v systemctl >/dev/null 2>&1 || continue
        rollback_step "DISABLE $path" systemctl disable "$path"
        ;;
      service)
        path="${fields[1]}"; active="${fields[2]}"
        command -v systemctl >/dev/null 2>&1 || continue
        if [[ "$active" == "1" ]]; then
          rollback_queue_restart "$path"
        else
          systemctl is-active --quiet "$path" 2>/dev/null || continue
          rollback_step "STOP $path" systemctl stop "$path"
        fi
        ;;
      ufw_added)
        port="${fields[1]}"
        command -v ufw >/dev/null 2>&1 || continue
        ufw status 2>/dev/null | grep -Eq "^[[:space:]]*${port//\//\\/}[[:space:]]+ALLOW([[:space:]]|$)" || continue
        rollback_step "REMOVE_UFW_RULE $port" ufw --force delete allow "$port"
        ;;
      ufw_deleted)
        port="${fields[1]}"
        command -v ufw >/dev/null 2>&1 || continue
        rollback_step "RESTORE_UFW_RULE $port" ufw allow "$port" comment 'Nexa Panel managed'
        ;;
      identity)
        existed="${fields[1]}"; roots="${fields[2]}"
        IFS=',' read -r -a created_roots <<< "$roots"
        for root in ${created_roots[@]+"${created_roots[@]}"}; do
          [[ -n "$root" ]] || continue
          case "$root" in
            /etc/nexa-panel|/var/lib/nexa-panel|/var/log/nexa-panel|/srv/nexa) ;;
            *) warn "refusing to remove unexpected managed root $root"; continue ;;
          esac
          [[ -d "$root" && ! -L "$root" ]] || continue
          rollback_step "REMOVE $root (this run created it)" rm -rf --one-file-system -- "$root"
        done
        if [[ "$existed" == "0" ]] && getent passwd nexa >/dev/null 2>&1; then
          rollback_step "REMOVE_SERVICE_ACCOUNT nexa" userdel nexa
          if getent group nexa >/dev/null 2>&1; then
            rollback_step "REMOVE_SERVICE_GROUP nexa" groupdel nexa
          fi
        fi
        ;;
      certificate)
        emit "RETAIN the TLS certificate obtained for ${fields[1]} (/etc/letsencrypt)"
        ;;
      security_repair)
        emit "RETAIN the removal of ${fields[1]} from the privileged ${fields[2]} group (a privilege repair is never undone)"
        ;;
      untouched|"")
        ;;
      *)
        warn "unknown rollback record '$action'"
        ;;
    esac
  done < <(tac "$ROLLBACK_JOURNAL")
  rollback_finalize
  if [[ "$ROLLBACK_FAILURES" -eq 0 ]]; then
    emit "Rollback complete: this run made no lasting change beyond the retentions listed above."
  else
    warn "rollback finished with $ROLLBACK_FAILURES failed step(s); this host needs manual inspection"
  fi
}

# on_failure runs for any non-zero exit. The log file is the point of the split,
# but it is not always reachable — inside a Docker build layer or a CI runner it
# disappears with the container — so the tail is replayed to the console rather
# than merely pointed at.
#
# Before any of that it unwinds the rollback journal, so a failure halfway
# through leaves the host as it was found rather than half-installed.
cleanup() {
  if [[ -n "${WORK_DIR:-}" && -d "${WORK_DIR:-}" ]]; then
    rm -rf -- "$WORK_DIR"
  fi
}

on_failure() {
  local status=$?
  trap - EXIT
  if [[ "$status" -eq 0 ]]; then
    cleanup
    [[ -n "$LOG_FILE" ]] && printf '\nTranscript: %s\n' "$LOG_FILE" >&8
    exit 0
  fi
  # A failure raised from inside the rollback itself must not restart it.
  if [[ "$ROLLBACK_IN_PROGRESS" -eq 0 ]]; then
    perform_rollback
  fi
  if [[ -n "$LOG_FILE" ]]; then
    {
      printf '\n'
      printf 'The install failed (exit %s). Last %s lines of the transcript:\n\n' "$status" "$LOG_TAIL_LINES"
      tail -n "$LOG_TAIL_LINES" "$LOG_FILE" 2>/dev/null || true
      printf '\nThe full transcript is at %s\n' "$LOG_FILE"
    } >&8
  fi
  # An incomplete rollback is the one case where the working directory is worth
  # more than the tidiness: it holds the journal and every saved original.
  if [[ "$ROLLBACK_FAILURES" -gt 0 ]]; then
    printf '\n%s\n' "$ROLLBACK_FAILURES rollback step(s) failed. The journal and the saved originals were kept at ${WORK_DIR:-(none)} — finish the recovery by hand (docs/runbooks/install.md)." >&8
  else
    cleanup
  fi
  exit "$status"
}

# start_transcript opens the log and hands stdout/stderr to it. mktemp rather than
# a fixed name: a predictable path in a world-writable directory is a symlink
# target on a shared host, and this file is written as root. 0600 because the
# transcript records the node's configuration.
start_transcript() {
  # A dry run writes nothing at all, transcript included.
  [[ "$VERBOSE" -eq 1 || "$DRY_RUN" -eq 1 ]] && return 0
  LOG_FILE="$(mktemp /var/log/nexa-panel-install.XXXXXXXX.log 2>/dev/null ||
    mktemp "${TMPDIR:-/tmp}/nexa-panel-install.XXXXXXXX.log")" || return 0
  chmod 0600 "$LOG_FILE"
  exec >>"$LOG_FILE" 2>&1
  trap on_failure EXIT
}

# The heredoc delimiter is quoted deliberately: the logo is drawn in slashes and
# backslashes, and an unquoted delimiter would let the shell eat every one of
# them. Printed only for an interactive install — a build layer, a --sync-packaging
# upgrade, or a piped log gets no banner.
show_logo() {
  # fd 8, not stdout: once the transcript is open stdout is a file, and the logo
  # belongs on the operator's terminal or nowhere.
  [[ -t 3 ]] || return 0
  cat >&8 <<"NEXA_LOGO"

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
  local source="$1" mode="$2" destination="$3" owner="${4:-root}" group="${5:-root}"
  local current_mode current_owner temporary directory
  current_mode="$(stat -c '%a' "$destination" 2>/dev/null || true)"
  current_owner="$(stat -c '%U:%G' "$destination" 2>/dev/null || true)"
  if [[ -f "$destination" ]] && cmp -s "$source" "$destination" &&
     [[ "$current_mode" == "${mode#0}" && "$current_owner" == "$owner:$group" ]]; then
    return 0
  fi
  journal_path "$destination"
  directory="$(dirname "$destination")"
  temporary="$(mktemp "$directory/.nexa-managed.XXXXXXXX")"
  if ! install -m "$mode" -o "$owner" -g "$group" "$source" "$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  sync -f "$temporary" 2>/dev/null || true
  mv -fT -- "$temporary" "$destination"
  sync -f "$directory" 2>/dev/null || true
  CHANGED=1
}

OWNERSHIP_MARKER="/var/lib/nexa-panel/install/ownership.v1"
OWNERSHIP_ADOPTION_PENDING=0
# API service drop-in. Two production settings the shipped unit deliberately does
# not hardcode: the first-run bootstrap token path (owner-only, under the state
# dir, not the /tmp dev default), and — only when serving non-loopback plaintext
# HTTP — the NEXA_ALLOW_INSECURE_HTTP opt-in the secure-transport guard requires.
NEXA_API_DROPIN_DIR="/etc/systemd/system/nexa-api.service.d"
NEXA_API_DROPIN="$NEXA_API_DROPIN_DIR/10-nexa-panel.conf"

ownership_marker_content() {
  cat <<'EOF'
format=1
owner=nexa-panel
service_user=nexa
service_home=/var/lib/nexa-panel
service_shell=/usr/sbin/nologin
root=/etc/nexa-panel
root=/var/lib/nexa-panel
root=/var/log/nexa-panel
root=/srv/nexa
EOF
}

validate_service_identity() {
  local account_record group_record account uid gid home shell group_gid
  account_record="$(getent passwd nexa 2>/dev/null || true)"
  group_record="$(getent group nexa 2>/dev/null || true)"
  [[ -n "$account_record" && -n "$group_record" ]] || return 1
  IFS=: read -r account _ uid gid _ home shell <<< "$account_record"
  IFS=: read -r _ _ group_gid _ <<< "$group_record"
  [[ "$account" == "nexa" && "$uid" != "0" && "$gid" == "$group_gid" &&
     "$home" == "/var/lib/nexa-panel" && "$shell" == "/usr/sbin/nologin" ]]
}

validate_owned_root_if_present() {
  local path="$1" owner_group="$2"
  [[ -e "$path" || -L "$path" ]] || return 0
  [[ -d "$path" && ! -L "$path" ]] || die "managed root $path is not a real directory; refusing to follow or replace it"
  [[ "$(stat -c '%U:%G' "$path")" == "$owner_group" ]] ||
    die "managed root $path has unexpected ownership (want $owner_group); refusing an ambiguous installation"
}

validate_or_plan_ownership() {
  local existing=0 marker_mode marker_owner expected_marker actual_marker path
  SERVICE_ACCOUNT_EXISTED=0
  if getent passwd nexa >/dev/null 2>&1 || getent group nexa >/dev/null 2>&1; then
    existing=1
    SERVICE_ACCOUNT_EXISTED=1
  fi
  # Which managed roots this run will be creating. Captured here, before the
  # first `install -d`, because "did this exist before we touched the host" is
  # the only question a rollback can safely act on.
  ABSENT_MANAGED_ROOTS=""
  for path in /etc/nexa-panel /var/lib/nexa-panel /var/log/nexa-panel /srv/nexa; do
    if [[ -e "$path" || -L "$path" ]]; then
      existing=1
    else
      ABSENT_MANAGED_ROOTS="${ABSENT_MANAGED_ROOTS:+$ABSENT_MANAGED_ROOTS,}$path"
    fi
  done

  if [[ -f "$OWNERSHIP_MARKER" && ! -L "$OWNERSHIP_MARKER" ]]; then
    marker_mode="$(stat -c '%a' "$OWNERSHIP_MARKER")"
    marker_owner="$(stat -c '%U:%G' "$OWNERSHIP_MARKER")"
    [[ "$marker_mode" == "600" && "$marker_owner" == "root:root" ]] ||
      die "the Nexa ownership marker must be root:root mode 0600"
    expected_marker="$(ownership_marker_content)"
    actual_marker="$(cat "$OWNERSHIP_MARKER")"
    [[ "$actual_marker" == "$expected_marker" ]] || die "the Nexa ownership marker is invalid or from an unsupported layout"
    validate_service_identity || die "the existing nexa service account/group does not match the recorded ownership contract"
  elif [[ "$existing" -eq 1 ]]; then
    [[ "$ADOPT_EXISTING" -eq 1 ]] ||
      die "pre-existing nexa identity or managed roots have no ownership marker; move them aside, or inspect the node and re-run once with --adopt-existing for a genuine pre-v1 Nexa install"
    validate_service_identity || die "--adopt-existing requires the exact nexa system account, primary group, home, and nologin shell"
    [[ -f /usr/bin/nexa && ! -L /usr/bin/nexa && "$(stat -c '%U:%G' /usr/bin/nexa)" == "root:root" ]] ||
      die "--adopt-existing requires a root-owned regular /usr/bin/nexa"
    [[ -f /usr/lib/systemd/system/nexa-agent.service && -f /usr/lib/systemd/system/nexa-api.service ]] ||
      die "--adopt-existing requires both installed Nexa systemd units"
    [[ -f /var/lib/nexa-panel/control.db && ! -L /var/lib/nexa-panel/control.db ]] ||
      die "--adopt-existing requires the existing control database"
    [[ -f /etc/nexa-panel/agent.token && ! -L /etc/nexa-panel/agent.token ]] ||
      die "--adopt-existing requires the existing agent credential"
    OWNERSHIP_ADOPTION_PENDING=1
  elif [[ "$ADOPT_EXISTING" -eq 1 ]]; then
    die "--adopt-existing was supplied, but no existing Nexa layout was found"
  fi

  validate_owned_root_if_present /etc/nexa-panel root:root
  validate_owned_root_if_present /var/lib/nexa-panel nexa:nexa
  validate_owned_root_if_present /var/log/nexa-panel nexa:nexa
  validate_owned_root_if_present /srv/nexa root:root
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
  local listen="$1" server_name="$2" destination="$3" template port
  template="$PACKAGING_DIR/nginx/nexa-panel.conf.template"
  if [[ "$listen" == 127.0.0.1:* ]]; then
    port="${listen##*:}"
    sed -e "s/^\( *\)listen __LISTEN__;/\1listen ${listen};\n\1listen [::1]:${port};/" \
        -e "s/__SERVER_NAME__/$server_name/g" \
        "$template" > "$destination"
    return 0
  fi
  if grep -q 'listen \[::\]' "$template"; then
    sed -e "s/__LISTEN__/$listen/g" -e "s/__SERVER_NAME__/$server_name/g" "$template" > "$destination"
    return 0
  fi
  sed -e "s/^\( *\)listen __LISTEN__;/\1listen __LISTEN__;\n\1listen [::]:__LISTEN__;/" \
      -e "s/__LISTEN__/$listen/g" \
      -e "s/__SERVER_NAME__/$server_name/g" \
      "$template" > "$destination"
}

# The listener and server name a rendered panel vhost carries. Read back rather
# than re-derived from flags, so a refresh can never change how a node is
# published; one definition, because two copies of these expressions would drift.
panel_vhost_listen() {
  sed -n 's/^[[:space:]]*listen[[:space:]]\{1,\}\([^;[:space:]]\{1,\}\).*;/\1/p' "$1" | head -n 1
}

panel_vhost_server_name() {
  sed -n 's/^[[:space:]]*server_name[[:space:]]\{1,\}\([^;]*\);.*/\1/p' "$1" | head -n 1
}

panel_vhost_is_tls() {
  grep -qsE '^[[:space:]]*listen[[:space:]].*443.*ssl' "$1"
}

# The ports this publishing mode needs open. Shared by the firewall reconciler
# and the dry-run plan so the plan cannot promise a different rule set.
panel_firewall_ports() {
  if [[ -n "$PANEL_HOSTNAME" && "$TLS_ACTIVE" -eq 1 ]]; then
    printf '%s\n' 80/tcp 443/tcp
  elif [[ -n "$PANEL_HOSTNAME" ]]; then
    printf '%s\n' 80/tcp
  elif [[ "$ALLOW_INSECURE" -eq 1 ]]; then
    printf '%s\n' 8888/tcp
  fi
}

# The URL the panel must answer on once this run has published it.
planned_panel_url() {
  local host
  if [[ -n "$PANEL_HOSTNAME" ]]; then
    if [[ "$TLS_ACTIVE" -eq 1 ]]; then
      printf 'https://%s/' "$PANEL_HOSTNAME"
    else
      printf 'http://%s/' "$PANEL_HOSTNAME"
    fi
  elif [[ "$INSECURE_HTTP" -eq 1 ]]; then
    host="$(detect_local_ip)"
    [[ -n "$host" ]] || host="127.0.0.1"
    printf 'http://%s:8888/' "$host"
  else
    printf 'http://127.0.0.1:8888/'
  fi
}

# The same URL, derived from the node itself rather than from this run's flags.
# --verify-ingress uses it to re-run the check on an installed node.
published_panel_url() {
  local vhost="/etc/nginx/sites-available/nexa-panel.conf" listen name port host
  [[ -f "$vhost" ]] || die "no panel vhost at $vhost: this node does not publish the panel"
  listen="$(panel_vhost_listen "$vhost")"
  name="$(panel_vhost_server_name "$vhost")"
  [[ -n "$listen" ]] || die "could not read the panel listener out of $vhost"
  if [[ -n "$name" && "$name" != "_" && "$name" != "localhost" ]]; then
    if panel_vhost_is_tls "$vhost"; then
      printf 'https://%s/' "$name"
    else
      printf 'http://%s/' "$name"
    fi
    return 0
  fi
  port="${listen##*:}"
  port="${port%% *}"
  if [[ "$listen" == 127.0.0.1:* || "$listen" == "[::1]:"* ]]; then
    host="127.0.0.1"
  else
    host="$(detect_local_ip)"
    [[ -n "$host" ]] || host="127.0.0.1"
  fi
  printf 'http://%s:%s/' "$host" "$port"
}

# verify_public_ingress URL — fetch the panel the way a browser will.
#
# The Unix-socket readiness probe only proves the API process is alive. It says
# nothing about whether Nginx is listening, whether the vhost being served is the
# one this run wrote, whether Certbot's rewrite still proxies to the panel, or
# whether the firewall lets the port through. An installation nobody can reach is
# a failed installation, so this is checked before success is reported.
verify_public_ingress() {
  local base="$1" url code attempt
  url="${base%/}/api/v1/health/live"
  log "Verifying public ingress at $url"
  code="000"
  for attempt in $(seq 1 "$INGRESS_ATTEMPTS"); do
    code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "$url" 2>/dev/null || printf '000')"
    if [[ "$code" == "200" ]]; then
      log "Public ingress answered HTTP 200 at $url"
      return 0
    fi
    [[ "$attempt" -eq "$INGRESS_ATTEMPTS" ]] || sleep "$INGRESS_DELAY"
  done
  die "the panel is not reachable through its published listener: $url answered ${code} (000 means no answer at all). Check that Nginx is running, that DNS for this hostname resolves to this host, and that nothing between the two is blocking the port."
}

# --- host checks ------------------------------------------------------------
# A dry run reads the host and writes nothing, so it does not need root: an
# operator should be able to review the plan before booking a maintenance window.
[[ "$DRY_RUN" -eq 1 || "${EUID:-$(id -u)}" -eq 0 ]] ||
  die "this installer must run as root (try: sudo ./scripts/install.sh ...); use --dry-run to inspect the plan"
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

# --verify-ingress is the installer's final check, offered on its own so an
# operator can re-run it after a DNS, certificate or firewall change without
# reinstalling. It reads the node and mutates nothing, so it takes no lifecycle
# lock and needs no packaging tree.
if [[ "$VERIFY_INGRESS_ONLY" -eq 1 ]]; then
  command -v curl >/dev/null 2>&1 || die "--verify-ingress needs curl"
  verify_public_ingress "$(published_panel_url)"
  exit 0
fi

# The whole node model is systemd units. A container or a minimal image without
# systemd-sysv will accept every step below and then fail at `systemctl enable`,
# leaving a half-configured host.
[[ -d /run/systemd/system ]] ||
  warn "systemd does not appear to be running (no /run/systemd/system); unit installation will be configured but cannot start"
if [[ "$START" -eq 0 && ( -n "$TLS_EMAIL" || "$MANAGE_FIREWALL" -eq 1 ) ]]; then
  die "--no-start cannot complete certificate or firewall activation; omit --no-start so the installer can verify the live ingress transaction"
fi
if [[ ! -d /run/systemd/system && ( -n "$TLS_EMAIL" || "$MANAGE_FIREWALL" -eq 1 ) ]]; then
  die "certificate and firewall activation require a running systemd host; this environment can only stage an unpublished/test image"
fi

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
# must therefore use TLS unless the operator explicitly accepts cleartext (for
# example, because TLS terminates at a load balancer).
if [[ -n "$PANEL_HOSTNAME" && -z "$TLS_EMAIL" && "$ALLOW_INSECURE" -eq 0 ]]; then
  die "refusing to publish $PANEL_HOSTNAME over plaintext HTTP: pass --tls-email EMAIL to provision TLS, or --allow-insecure-http to accept cleartext authentication (only safe when TLS terminates in front of this node)"
fi

# A flagless re-run must not silently replace a TLS vhost with a plaintext
# listener. Preserve an existing ingress unless the operator explicitly asks to
# reconfigure it. Fresh flagless installs are loopback-only.
PRESERVE_INGRESS=0
if [[ "$MODE" == "install" && "$INGRESS_EXPLICIT" -eq 0 && -f /etc/nginx/sites-available/nexa-panel.conf ]]; then
  PRESERVE_INGRESS=1
fi
INSECURE_HTTP="$ALLOW_INSECURE"
if [[ "$PRESERVE_INGRESS" -eq 1 ]]; then
  if grep -qs '^Environment=NEXA_ALLOW_INSECURE_HTTP=1$' "$NEXA_API_DROPIN"; then
    INSECURE_HTTP=1
  else
    INSECURE_HTTP=0
  fi
fi

WORK_DIR="$(mktemp -d)"
trap on_failure EXIT
journal_start

# --- release credential -----------------------------------------------------
# Optional by design. Without a token the same GitHub API URLs are used
# anonymously, so nothing here has to change if the repository is ever made
# public. The token is only ever held in a variable, handed to curl on stdin
# (never on a command line, where the process table would leak it), and written
# to a 0600 root-owned file.
GITHUB_TOKEN="${NEXA_GITHUB_TOKEN:-}"
unset NEXA_GITHUB_TOKEN
# A variable inherited from the environment retains Bash's export attribute
# across assignment. Explicitly clear it so repository credentials cannot leak
# into apt, systemctl, certbot, or seed-helper child processes.
export -n GITHUB_TOKEN 2>/dev/null || true
GITHUB_TOKEN_SOURCE="NEXA_GITHUB_TOKEN"
if [[ -n "$GITHUB_TOKEN_FILE" ]]; then
  [[ -f "$GITHUB_TOKEN_FILE" && ! -L "$GITHUB_TOKEN_FILE" ]] || die "the GitHub token path must be a regular file, not a symlink"
  TOKEN_OWNER="$(stat -c '%u' "$GITHUB_TOKEN_FILE")"
  TOKEN_MODE="$(stat -c '%a' "$GITHUB_TOKEN_FILE")"
  TOKEN_SIZE="$(stat -c '%s' "$GITHUB_TOKEN_FILE")"
  [[ "$TOKEN_OWNER" == 0 ]] || die "the GitHub token file must be owned by root"
  (( (8#$TOKEN_MODE & 077) == 0 )) || die "the GitHub token file must not be readable or writable by group/other (use chmod 0600)"
  (( TOKEN_SIZE > 0 && TOKEN_SIZE <= 16384 )) || die "the GitHub token file must contain 1-16384 bytes"
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

# release_asset_url METADATA NAME — resolve an exact GitHub release asset with a
# real JSON parser. String-splitting JSON breaks on legal whitespace and field
# ordering and can select the wrong object.
release_asset_url() {
  local metadata="$1" name="$2"
  python3 "$RELEASE_HELPER" asset-url "$metadata" "$name"
}

# Fetch, verify and unpack the release for this host, then install out of the
# unpacked copy. Nothing from the network is executed — or written anywhere but
# the temporary directory — until the sha256 sidecar matches.
bootstrap_from_release() {
  local arch tarball checksum signature endpoint metadata code
  local tarball_url checksum_url signature_url unpacked installer expected actual status
  command -v curl >/dev/null 2>&1 || die "the remote bootstrap needs curl; install it with: apt-get install -y curl"
  command -v python3 >/dev/null 2>&1 || die "the remote bootstrap needs Python 3 (Ubuntu 24.04 includes it)"
  command -v ssh-keygen >/dev/null 2>&1 || die "the remote bootstrap needs ssh-keygen from openssh-client"
  [[ -x "$RELEASE_HELPER" ]] || die "release helper missing at $RELEASE_HELPER; run --download from the complete source/bootstrap bundle"
  [[ -s "$RELEASE_SIGNERS" ]] || die "trusted release signers missing at $RELEASE_SIGNERS"
  arch="$(dpkg --print-architecture)"
  case "$arch" in
    amd64|arm64) ;;
    *) die "no Nexa Panel release is published for this architecture ($arch)" ;;
  esac
  tarball="nexa-panel-linux-${arch}.tar.gz"
  checksum="${tarball}.sha256"
  signature="${tarball}.sig"
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
  signature_url="$(release_asset_url "$metadata" "$signature")"
  [[ -n "$signature_url" ]] || die "${RELEASE_VERSION:-the latest release} of $RELEASE_REPO publishes no $signature, so its publisher cannot be authenticated"

  log "Downloading $tarball"
  code="$(github_fetch "$tarball_url" "application/octet-stream" "$WORK_DIR/$tarball")"
  release_http_check "$code" "$tarball"
  code="$(github_fetch "$checksum_url" "application/octet-stream" "$WORK_DIR/$checksum")"
  release_http_check "$code" "$checksum"
  code="$(github_fetch "$signature_url" "application/octet-stream" "$WORK_DIR/$signature")"
  release_http_check "$code" "$signature"

  expected="$(awk 'NR==1{print $1}' "$WORK_DIR/$checksum")"
  [[ -n "$expected" ]] || die "$checksum carries no checksum"
  actual="$(sha256sum "$WORK_DIR/$tarball" | awk '{print $1}')"
  [[ "$expected" == "$actual" ]] || die "checksum mismatch for $tarball: expected $expected, got $actual. The download was truncated or tampered with; nothing has been installed."
  if ! ssh-keygen -Y verify -f "$RELEASE_SIGNERS" -I nexa-panel-release -n file \
       -s "$WORK_DIR/$signature" < "$WORK_DIR/$tarball" >/dev/null; then
    die "publisher signature verification failed for $tarball; nothing has been installed"
  fi
  log "Verified publisher signature and sha256 for $tarball ($actual)"

  unpacked="$WORK_DIR/unpacked"
  installer="$(python3 "$RELEASE_HELPER" extract "$WORK_DIR/$tarball" "$unpacked")" ||
    die "$tarball failed safe extraction; nothing has been installed"

  log "Installing from the unpacked release"
  status=0
  bash "$installer" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"} || status=$?
  exit "$status"
}

if [[ "$DOWNLOAD" -eq 1 ]]; then
  bootstrap_from_release
fi

# Downloading and verifying a release is read-only host work. Acquire the
# lifecycle lock only after the bootstrap handoff: the unpacked installer is a
# new process that must acquire the lock itself, and retaining it in this parent
# while waiting would deadlock every `--download` installation.
#
# A dry run takes no lock either: it changes nothing, and an operator reviewing
# the plan must not be able to block a running install by doing so.
if [[ "$DRY_RUN" -eq 0 ]]; then
  install -d -m 0755 /run/lock
  if [[ -n "${NEXA_LIFECYCLE_LOCK_FD:-}" ]]; then
    [[ "$NEXA_LIFECYCLE_LOCK_FD" =~ ^[0-9]+$ && "$NEXA_LIFECYCLE_LOCK_FD" -ge 3 ]] ||
      die "the inherited lifecycle lock descriptor is invalid"
    [[ -e "/proc/$$/fd/$NEXA_LIFECYCLE_LOCK_FD" && "/proc/$$/fd/$NEXA_LIFECYCLE_LOCK_FD" -ef "$LIFECYCLE_LOCK_PATH" ]] ||
      die "the inherited lifecycle lock descriptor does not reference $LIFECYCLE_LOCK_PATH"
    flock -n "$NEXA_LIFECYCLE_LOCK_FD" || die "another Nexa Panel install, update, or uninstall is running"
    unset NEXA_LIFECYCLE_LOCK_FD
  else
    exec 9>"$LIFECYCLE_LOCK_PATH"
    flock -n 9 || die "another Nexa Panel install, update, or uninstall is running"
  fi
fi

# --- layout -----------------------------------------------------------------
# Everything the installer reads is resolved relative to this script, never to a
# repository root: that is the whole reason a source checkout, an unpacked
# release tarball, and the copy the remote bootstrap unpacks are interchangeable.
# A release bundle also carries the binary, so `--binary` is optional there.
PACKAGING_DIR="$ROOT_DIR/packaging"
SEED_SCRIPT="$ROOT_DIR/scripts/nexa-seed-admin.sh"
UNINSTALL_SCRIPT="$ROOT_DIR/scripts/uninstall.sh"
[[ -f "$PACKAGING_DIR/systemd/nexa-api.service" ]] || die "no packaging tree at $PACKAGING_DIR: run this script from a source checkout or an unpacked release tarball, or pass --download to fetch one"
[[ -x "$UNINSTALL_SCRIPT" ]] || die "no executable uninstaller at $UNINSTALL_SCRIPT; use a complete source checkout or release bundle"
RELEASE_LABEL=""
if [[ -f "$ROOT_DIR/RELEASE" ]]; then
  RELEASE_LABEL="$(sed -n 's/^version=//p' "$ROOT_DIR/RELEASE" | head -n 1)"
  if [[ "$MODE" == "install" && -z "$BINARY" && -f "$ROOT_DIR/bin/nexa" ]]; then
    BINARY="$ROOT_DIR/bin/nexa"
  fi
fi

# --- inventories ------------------------------------------------------------
# One declaration each for the things the plan has to be able to name and the
# install has to actually do. The dry-run plan reads exactly these, so it cannot
# promise a file, package or unit the install does not touch — a second, prose
# copy of this list is how a plan starts lying.
PREREQUISITE_PACKAGES=(
  systemd systemd-sysv dbus
  nginx cron certbot python3-certbot-nginx
  logrotate
  postgresql-common libjson-perl
  passwd util-linux
  ufw
  rclone
  openssh-server
  git unzip rsync acl sudo
  podman fuse-overlayfs
  ca-certificates curl gnupg software-properties-common
)

PHP_PACKAGES=()
if [[ "$INSTALL_PHP" -eq 1 ]]; then
  PHP_PACKAGES=(
    "php${PHP_VERSION}-fpm" "php${PHP_VERSION}-cli"
    "php${PHP_VERSION}-mysql" "php${PHP_VERSION}-pgsql"
    "php${PHP_VERSION}-mbstring" "php${PHP_VERSION}-xml"
    "php${PHP_VERSION}-curl" "php${PHP_VERSION}-zip"
    "php${PHP_VERSION}-gd" "php${PHP_VERSION}-intl" "php${PHP_VERSION}-bcmath"
  )
fi

# SOURCE|MODE|DESTINATION. The same set uninstall.sh removes; keep the two in
# step when either changes.
MANAGED_PACKAGING_FILES=(
  "$PACKAGING_DIR/systemd/nexa-agent.service|0644|/usr/lib/systemd/system/nexa-agent.service"
  "$PACKAGING_DIR/systemd/nexa-api.service|0644|/usr/lib/systemd/system/nexa-api.service"
  "$PACKAGING_DIR/systemd/nexa-panel-system-backup.service|0644|/usr/lib/systemd/system/nexa-panel-system-backup.service"
  "$PACKAGING_DIR/systemd/nexa-panel-system-backup.timer|0644|/usr/lib/systemd/system/nexa-panel-system-backup.timer"
  "$PACKAGING_DIR/systemd/nexa-update-recovery.service|0644|/usr/lib/systemd/system/nexa-update-recovery.service"
  "$PACKAGING_DIR/sysusers/nexa-panel.conf|0644|/usr/lib/sysusers.d/nexa-panel.conf"
  "$PACKAGING_DIR/tmpfiles/nexa-panel.conf|0644|/usr/lib/tmpfiles.d/nexa-panel.conf"
  "$PACKAGING_DIR/release-signers|0644|/etc/nexa-panel/release-signers"
  "$UNINSTALL_SCRIPT|0755|/usr/lib/nexa-panel/uninstall.sh"
)

MANAGED_DIRECTORIES=(
  /usr/lib/systemd/system /usr/lib/sysusers.d /usr/lib/tmpfiles.d
  /usr/lib/nexa-panel /usr/sbin /etc/nexa-panel
  /etc/nginx/sites-available /etc/nginx/sites-enabled /etc/nginx/snippets
  /etc/ssh/sshd_config.d /run/sshd
)

ENABLED_UNITS=(
  nexa-agent.service nexa-api.service nginx.service cron.service ssh.service
  nexa-panel-system-backup.timer nexa-update-recovery.service
)

# print_install_plan — the complete --dry-run plan, in execution order, using the
# same verbs as uninstall.sh so both halves of the lifecycle read alike.
print_install_plan() {
  local entry source mode destination unit package port host vhost
  emit "Nexa Panel install plan (dry run)"
  emit "MODE $MODE"
  emit "RELEASE ${RELEASE_LABEL:-unknown}"
  emit "HOST Ubuntu ${VERSION_ID:-?} (${VERSION_CODENAME:-?}) $HOST_ARCH"
  if [[ "$MODE" == "sync-packaging" || "$PRESERVE_INGRESS" -eq 1 ]]; then
    vhost="/etc/nginx/sites-available/nexa-panel.conf"
    if [[ -f "$vhost" ]]; then
      emit "PUBLISH preserved: listen $(panel_vhost_listen "$vhost"), server_name $(panel_vhost_server_name "$vhost")"
    else
      emit "PUBLISH nothing (no existing panel vhost to preserve)"
    fi
  elif [[ -n "$PANEL_HOSTNAME" ]]; then
    emit "PUBLISH $PANEL_HOSTNAME on :80$([[ "$TLS_ACTIVE" -eq 1 ]] && echo ' with TLS (certbot)' || echo ' over plaintext HTTP')"
  elif [[ "$ALLOW_INSECURE" -eq 1 ]]; then
    emit "PUBLISH all interfaces on :8888 over plaintext HTTP"
  else
    emit "PUBLISH loopback only on 127.0.0.1:8888 (reach it through an SSH tunnel)"
  fi

  if [[ -n "$BINARY" ]]; then
    emit "RUN $BINARY version (validate the candidate before anything is changed)"
  fi
  if [[ "$MODE" != "sync-packaging" && "$SKIP_PREFLIGHT" != "1" ]]; then
    emit "RUN nexa doctor --preflight"
  fi

  if [[ "$MODE" != "sync-packaging" ]]; then
    emit "RUN apt-get update"
    for package in "${PREREQUISITE_PACKAGES[@]}"; do
      emit "INSTALL_PACKAGE $package"
    done
    if grep -rqs 'ondrej' /etc/apt/sources.list.d/ 2>/dev/null; then
      emit "RETAIN the already-configured PHP repository (ppa:ondrej/php)"
    else
      emit "RUN add-apt-repository -y ppa:ondrej/php"
    fi
    if grep -rqs 'apt.postgresql.org' /etc/apt/sources.list.d/ 2>/dev/null; then
      emit "RETAIN the already-configured PostgreSQL repository (PGDG)"
    else
      emit "RUN /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y"
    fi
  else
    emit "RETAIN every Ubuntu package and repository (a packaging refresh installs none)"
  fi
  for package in ${PHP_PACKAGES[@]+"${PHP_PACKAGES[@]}"}; do
    emit "INSTALL_PACKAGE $package"
  done
  if [[ "$INSTALL_COMPOSER" -eq 1 ]]; then
    emit "INSTALL_PACKAGE composer"
  fi

  for destination in "${MANAGED_DIRECTORIES[@]}"; do
    emit "MKDIR $destination"
  done
  for entry in "${MANAGED_PACKAGING_FILES[@]}"; do
    IFS='|' read -r source mode destination <<< "$entry"
    emit "WRITE $destination (mode $mode)"
  done
  emit "SYMLINK /usr/sbin/nexa-uninstall -> /usr/lib/nexa-panel/uninstall.sh"
  if [[ ( "$MODE" == "sync-packaging" || "$PRESERVE_INGRESS" -eq 1 ) && -f "$NEXA_API_DROPIN" ]]; then
    emit "RETAIN $NEXA_API_DROPIN"
  else
    emit "WRITE $NEXA_API_DROPIN (mode 0644)"
  fi
  if [[ "$MODE" != "sync-packaging" ]]; then
    emit "RUN systemd-sysusers (create the nexa service account)"
    emit "RUN systemd-tmpfiles --create (create /etc/nexa-panel /var/lib/nexa-panel /var/log/nexa-panel /srv/nexa)"
  else
    emit "RETAIN the nexa service account and the managed directory tree"
  fi
  emit "WRITE $OWNERSHIP_MARKER (mode 0600)"
  if [[ -n "$GITHUB_TOKEN" && "$SAVE_TOKEN" -eq 1 ]]; then
    emit "WRITE $RELEASE_TOKEN_PATH (mode 0600)"
  fi
  emit "WRITE /etc/nginx/snippets/nexa-panel-proxy.conf (mode 0644)"
  emit "WRITE /etc/nginx/sites-available/nexa-panel.conf (mode 0644)"
  emit "SYMLINK /etc/nginx/sites-enabled/nexa-panel.conf -> /etc/nginx/sites-available/nexa-panel.conf"
  if [[ "$MODE" != "sync-packaging" && -L /etc/nginx/sites-enabled/default ]]; then
    emit "REMOVE /etc/nginx/sites-enabled/default (restore target recorded)"
  fi
  emit "RUN nginx -t"
  if [[ "$MODE" != "sync-packaging" ]] && ! grep -qsE '^\s*Include\s+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config; then
    emit "APPEND /etc/ssh/sshd_config (Include /etc/ssh/sshd_config.d/*.conf)"
  else
    emit "RETAIN /etc/ssh/sshd_config"
  fi
  if [[ "$MODE" != "sync-packaging" ]]; then
    emit "RUN ssh-keygen -A"
  fi
  emit "RUN sshd -t"
  if [[ -n "$BINARY" ]]; then
    emit "WRITE /usr/bin/nexa (mode 0755) from $BINARY"
  else
    emit "RETAIN /usr/bin/nexa"
  fi

  if [[ "$MODE" != "sync-packaging" ]]; then
    for unit in "${ENABLED_UNITS[@]}"; do
      emit "ENABLE $unit"
    done
    if [[ "$INSTALL_PHP" -eq 1 ]]; then
      emit "ENABLE php${PHP_VERSION}-fpm.service"
    fi
  fi
  if [[ "$START" -eq 0 ]]; then
    emit "RETAIN every service state (--no-start)"
  else
    emit "RUN systemctl daemon-reload"
    emit "RUN systemctl restart nexa-agent.service nexa-api.service"
    emit "RUN systemctl reload-or-start nginx.service"
    if [[ "$MODE" != "sync-packaging" ]]; then
      emit "RUN systemctl start nexa-panel-system-backup.timer"
    fi
  fi

  if [[ "$MODE" != "sync-packaging" && "$START" -eq 1 ]]; then
    if [[ "$MANAGE_FIREWALL" -eq 1 ]]; then
      while IFS= read -r port; do
        [[ -n "$port" ]] || continue
        emit "RUN ufw allow $port comment 'Nexa Panel managed'"
      done < <(panel_firewall_ports)
      emit "RETAIN the UFW default policy and every rule the panel does not own"
    else
      emit "RETAIN the host firewall entirely (--manage-firewall was not given)"
    fi
    if [[ -n "$TLS_EMAIL" ]]; then
      emit "RUN certbot --nginx --non-interactive --agree-tos --redirect --email $TLS_EMAIL -d $PANEL_HOSTNAME"
    fi
    emit "RUN nexa-seed-admin.sh (create the first administrator if none exists)"
    emit "VERIFY $(planned_panel_url)api/v1/health/live"
  fi

  emit "RETAIN hosted sites, databases, backups, panel state, and TLS material"
  emit ""
  emit "Dry run only: no package, file, unit, firewall rule or service was changed."
}

export DEBIAN_FRONTEND=noninteractive
start_transcript
if [[ "$MODE" == "sync-packaging" ]]; then
  log "Refreshing Nexa Panel packaging${RELEASE_LABEL:+ ($RELEASE_LABEL)} on Ubuntu ${VERSION_ID:-?} (${VERSION_CODENAME}), $(dpkg --print-architecture)"
else
  show_logo
  log "Installing Nexa Panel${RELEASE_LABEL:+ $RELEASE_LABEL} on Ubuntu ${VERSION_ID:-?} (${VERSION_CODENAME}), $(dpkg --print-architecture)"
fi

# --- candidate validation ---------------------------------------------------
CANDIDATE_BINARY=""
if [[ -n "$BINARY" ]]; then
  [[ -f "$BINARY" ]] || die "no binary at $BINARY"
  [[ ! -L "$BINARY" ]] || die "refusing a symlink as --binary; pass the resolved regular file"
  CANDIDATE_BINARY="$WORK_DIR/nexa.candidate"
  log "Validating the staged candidate $BINARY"
  install -m 0755 "$BINARY" "$CANDIDATE_BINARY"
  if ! "$CANDIDATE_BINARY" version >/dev/null 2>&1; then
    die "$BINARY is not an executable Nexa Panel binary for this node"
  fi
  # A binary compiled without `-tags embed` runs, reports a version, and serves
  # no web UI at all — the SPA route is simply absent, so the panel answers every
  # browser request with a 404 and looks broken for reasons nothing explains.
  # The embedded asset tree is addressed as dist/index.html inside the binary, so
  # its absence is a reliable, cheap proof that the frontend was left out.
  if ! LC_ALL=C grep -qa 'dist/index.html' "$CANDIDATE_BINARY"; then
    die "$BINARY carries no embedded web UI, so the panel would serve no interface: build releases with scripts/build-linux-release.sh (which compiles with -tags embed), not a plain \`go build\`"
  fi
elif [[ ! -x /usr/bin/nexa ]]; then
  [[ "$START" -eq 0 || ! -d /run/systemd/system ]] || die "no Nexa Panel binary to start; pass --binary PATH"
  warn "no nexa binary at /usr/bin/nexa — install one before starting the enabled services"
fi

# Establish exclusive ownership before apt, sysusers, tmpfiles, or any managed
# write can adopt a coincidentally named account/path. Existing v1 installs carry
# the marker; pre-v1 layouts require one explicit, fully validated adoption.
validate_or_plan_ownership

# The plan is printed here: after every read-only validation (host, ownership,
# candidate binary) has had its say, and before the first host mutation.
if [[ "$DRY_RUN" -eq 1 ]]; then
  print_install_plan
  exit 0
fi

# --- preflight --------------------------------------------------------------
# Last chance to abort cleanly. The staged candidate runs the checks, so a failed
# preflight leaves the installed binary and every host configuration untouched.
#
# A packaging refresh runs on a node that is by definition already installed and
# serving, which every one of those checks would report as a conflict, so it
# skips them.
if [[ "$MODE" == "sync-packaging" ]]; then
  :
elif [[ "$SKIP_PREFLIGHT" == "1" ]]; then
  warn "skipping the preflight checks (--skip-preflight); conflicts on this host will surface as install failures instead"
elif [[ -z "$CANDIDATE_BINARY" && ! -x /usr/bin/nexa ]]; then
  warn "no nexa binary yet, so the preflight checks cannot run; pass --binary PATH to have them checked"
else
  log "Running preflight checks"
  PREFLIGHT_BINARY="${CANDIDATE_BINARY:-/usr/bin/nexa}"
  PREFLIGHT_ARGS=(doctor --preflight)
  if [[ "$ALLOW_EXISTING" -eq 1 ]]; then
    PREFLIGHT_ARGS+=(--allow-existing)
  fi
  if ! "$PREFLIGHT_BINARY" "${PREFLIGHT_ARGS[@]}"; then
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
if [[ "$MODE" != "sync-packaging" ]]; then
  log "Refreshing the package index"
  apt-get update -qq

  log "Installing host prerequisites"
  # Journalled before the transaction, not after: a dpkg run that fails partway
  # has still changed the host, and the rollback has to say so.
  journal packages prerequisites
  apt-get install -y --no-install-recommends "${PREREQUISITE_PACKAGES[@]}"
else
  # An update transaction cannot roll back dpkg. New OS dependencies therefore
  # block during release qualification instead of being installed halfway
  # through activation.
  for required_command in nginx systemctl systemd-sysusers systemd-tmpfiles sshd ssh-keygen curl podman; do
    command -v "$required_command" >/dev/null 2>&1 ||
      die "packaging update requires $required_command, which is not installed; install the release prerequisite before retrying"
  done
fi

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
if [[ "$MODE" != "sync-packaging" ]] && ! podman info >/dev/null 2>&1 && podman info 2>&1 | grep -q "not supported over overlayfs"; then
  log "Native overlay unavailable (overlayfs backing filesystem); enabling fuse-overlayfs for Podman"
  ensure_directory 0755 /etc/containers
  journal_path /etc/containers/storage.conf
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
#
# Adding a repository leaves files behind in three package-manager directories,
# and which files those are is upstream's business, not this script's. Snapshot
# them, add the repository, and journal whatever appeared — so the undo removes
# exactly what the addition created and nothing a previous install left.
apt_source_snapshot() {
  find /etc/apt/sources.list.d /etc/apt/trusted.gpg.d /usr/share/keyrings \
    -maxdepth 1 \( -type f -o -type l \) 2>/dev/null | sort
}

journal_new_apt_sources() {
  local before="$1" path
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    journal apt_source "$path"
  done < <(comm -13 "$before" <(apt_source_snapshot))
}

REPOSITORIES_ADDED=0
if [[ "$MODE" != "sync-packaging" ]] && ! grep -rqs 'ondrej' /etc/apt/sources.list.d/ 2>/dev/null; then
  log "Configuring the PHP repository (ppa:ondrej/php)"
  apt_source_snapshot > "$WORK_DIR/apt-sources.before"
  add-apt-repository -y ppa:ondrej/php
  journal_new_apt_sources "$WORK_DIR/apt-sources.before"
  REPOSITORIES_ADDED=1
fi

if [[ "$MODE" != "sync-packaging" ]] && ! grep -rqs 'apt.postgresql.org' /etc/apt/sources.list.d/ 2>/dev/null; then
  log "Configuring the PostgreSQL repository (PGDG)"
  apt_source_snapshot > "$WORK_DIR/apt-sources.before"
  /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
  journal_new_apt_sources "$WORK_DIR/apt-sources.before"
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
  journal packages php
  apt-get install -y --no-install-recommends "${PHP_PACKAGES[@]}"
  journal_unit_enablement "php${PHP_VERSION}-fpm.service"
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
  journal packages composer
  apt-get install -y --no-install-recommends composer
elif [[ "$MODE" != "sync-packaging" && "$COMPOSER_EXPLICIT" -eq 1 ]]; then
  warn "no Composer installed (--no-composer): deployments that need it install it on demand"
fi

# --- packaged units and configuration ---------------------------------------
log "Installing the packaged units, service account, and directories"
for managed_directory in /usr/lib/systemd/system /usr/lib/sysusers.d /usr/lib/tmpfiles.d /usr/lib/nexa-panel /usr/sbin /etc/nexa-panel; do
  ensure_directory 0755 "$managed_directory"
done
for managed_entry in "${MANAGED_PACKAGING_FILES[@]}"; do
  IFS='|' read -r managed_source managed_mode managed_destination <<< "$managed_entry"
  install_managed "$managed_source" "$managed_mode" "$managed_destination"
done
journal_path /usr/sbin/nexa-uninstall
ln -sfn /usr/lib/nexa-panel/uninstall.sh /usr/sbin/nexa-uninstall

# The drop-in is written every run so a later re-install without cleartext drops
# the override; its paths are declared with the other inventories above.
ensure_directory 0755 "$NEXA_API_DROPIN_DIR"
# Retire the older single-purpose drop-in name so its setting cannot linger.
journal_path "$NEXA_API_DROPIN_DIR/10-insecure-http.conf"
rm -f "$NEXA_API_DROPIN_DIR/10-insecure-http.conf"
# The drop-in records how this node was published. A packaging refresh must not
# rewrite that from flags it was not given, so an existing one is left exactly as
# the install wrote it.
if [[ ( "$MODE" == "sync-packaging" || "$PRESERVE_INGRESS" -eq 1 ) && -f "$NEXA_API_DROPIN" ]]; then
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

# Account/directory creation is an install-time mutation. An update refreshes
# their declarations but does not create or re-own arbitrary state outside its
# snapshotted file set.
if [[ "$MODE" == "sync-packaging" ]]; then
  id nexa >/dev/null 2>&1 || die "the nexa service account is missing; repair the installation before updating"
else
  # The account and the managed roots are journalled together, because they have
  # to be undone together: an abandoned `nexa` account or an abandoned managed
  # root is exactly what makes the next run refuse to install ("no ownership
  # marker"). Both readings were taken before the first mutation, so only what
  # this run creates is ever removed.
  journal identity "$SERVICE_ACCOUNT_EXISTED" "$ABSENT_MANAGED_ROOTS"
  systemd-sysusers
  systemd-tmpfiles --create
fi

# Record the namespace this installation owns. The marker is intentionally
# outside the executable/package tree so retain-data uninstall/reinstall keeps
# proof of ownership, while purge removes it with panel state.
ensure_directory 0700 /var/lib/nexa-panel/install root root
ownership_marker_content > "$WORK_DIR/ownership.v1"
install_managed "$WORK_DIR/ownership.v1" 0600 "$OWNERSHIP_MARKER" root root
if [[ "$OWNERSHIP_ADOPTION_PENDING" -eq 1 ]]; then
  log "Recorded the validated pre-v1 Nexa layout ownership marker"
fi

# Nginx receives access only to the API ingress socket through its dedicated
# runtime directory/group contract. It must never join `nexa`, which can reach
# the root agent socket and credential.
if id -nG www-data 2>/dev/null | tr ' ' '\n' | grep -qx nexa; then
  # Journalled as a repair, not as a change: a rollback re-granting the web
  # server access to the root agent's group would undo a security fix to recover
  # from an unrelated failure.
  journal security_repair www-data nexa
  gpasswd -d www-data nexa >/dev/null 2>&1 || die "could not remove legacy www-data membership from the privileged nexa group"
  CHANGED=1
  NGINX_HARD_RESTART=1
fi

# Hand the release credential to the node. `nexa self-update` fetches later
# releases from the same private repository and reads exactly this path, so an
# operator who supplied a token once never has to supply it again. Root-only:
# it is a repository credential, and the API account has no business reading it.
if [[ -n "$GITHUB_TOKEN" && "$SAVE_TOKEN" -eq 1 ]]; then
  log "Storing the release credential in $RELEASE_TOKEN_PATH"
  (umask 077; printf '%s\n' "$GITHUB_TOKEN" > "$WORK_DIR/release.token")
  install_managed "$WORK_DIR/release.token" 0600 "$RELEASE_TOKEN_PATH" root root
fi

log "Configuring the panel reverse proxy"
for managed_directory in /etc/nginx/sites-available /etc/nginx/sites-enabled /etc/nginx/snippets; do
  ensure_directory 0755 "$managed_directory"
done
PANEL_VHOST="/etc/nginx/sites-available/nexa-panel.conf"
PANEL_PROXY_SNIPPET="/etc/nginx/snippets/nexa-panel-proxy.conf"
install_managed "$PACKAGING_DIR/nginx/nexa-panel-proxy.conf.template" 0644 "$PANEL_PROXY_SNIPPET"
if [[ "$MODE" == "sync-packaging" || "$PRESERVE_INGRESS" -eq 1 ]]; then
  # Refreshing the vhost must not change how the panel is published, so the
  # listener and server name are read back out of the file the install wrote
  # rather than re-derived from flags this run was never given. A legacy vhost
  # Certbot has rewritten is migrated structurally: only the old Nexa proxy
  # directives move into the managed snippet; its TLS listeners, certificates,
  # and redirects remain byte-for-byte under Certbot's ownership.
  if [[ ! -f "$PANEL_VHOST" ]]; then
    warn "no panel vhost at $PANEL_VHOST; run the installer without --sync-packaging to publish the panel"
  elif grep -q 'managed by Certbot' "$PANEL_VHOST"; then
    if grep -Fq 'include /etc/nginx/snippets/nexa-panel-proxy.conf;' "$PANEL_VHOST"; then
      log "The certbot-managed panel vhost already uses the managed proxy snippet"
    else
      log "Migrating the certbot-managed panel vhost to the managed proxy snippet"
      cp -p -- "$PANEL_VHOST" "$WORK_DIR/nexa-panel.certbot.original"
      python3 "$RELEASE_HELPER" migrate-nginx-vhost "$PANEL_VHOST" "$WORK_DIR/nexa-panel.certbot.migrated" ||
        die "could not safely migrate the certbot-managed panel vhost"
      install_managed "$WORK_DIR/nexa-panel.certbot.migrated" 0644 "$PANEL_VHOST"
      if ! nginx -t; then
        warn "the migrated certbot-managed panel vhost failed validation; restoring the original"
        install_managed "$WORK_DIR/nexa-panel.certbot.original" 0644 "$PANEL_VHOST"
        nginx -t || die "the original certbot-managed panel vhost also fails Nginx validation"
        die "certbot-managed panel vhost migration failed Nginx validation"
      fi
    fi
  else
    PANEL_LISTEN="$(panel_vhost_listen "$PANEL_VHOST")"
    PANEL_SERVER_NAME="$(panel_vhost_server_name "$PANEL_VHOST")"
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
  elif [[ "$ALLOW_INSECURE" -eq 1 ]]; then
    PANEL_LISTEN="8888"
    PANEL_SERVER_NAME="_"
  else
    # Secure bootstrap default: use an SSH tunnel (`ssh -L 8888:localhost:8888`)
    # until a hostname and certificate are configured.
    PANEL_LISTEN="127.0.0.1:8888"
    PANEL_SERVER_NAME="localhost"
  fi
  render_panel_vhost "$PANEL_LISTEN" "$PANEL_SERVER_NAME" "$WORK_DIR/nexa-panel.conf"
  install_managed "$WORK_DIR/nexa-panel.conf" 0644 "$PANEL_VHOST"
fi
if [[ "$PRESERVE_INGRESS" -eq 1 && -f "$PANEL_VHOST" ]]; then
  PRESERVED_SERVER_NAME="$(panel_vhost_server_name "$PANEL_VHOST")"
  if [[ -n "$PRESERVED_SERVER_NAME" && "$PRESERVED_SERVER_NAME" != "_" && "$PRESERVED_SERVER_NAME" != "localhost" ]]; then
    PANEL_HOSTNAME="$PRESERVED_SERVER_NAME"
  fi
  if panel_vhost_is_tls "$PANEL_VHOST"; then
    TLS_ACTIVE=1
  fi
fi
# Only ever link a vhost that exists: a --sync-packaging run on a node the panel
# was never published on has nothing to link, and a dangling symlink in
# sites-enabled fails `nginx -t` outright, taking every other site down with it.
if [[ -f "$PANEL_VHOST" ]]; then
  journal_path /etc/nginx/sites-enabled/nexa-panel.conf
  ln -sfn "$PANEL_VHOST" /etc/nginx/sites-enabled/nexa-panel.conf
fi

# Ubuntu's nginx package enables a stock `default` site that answers on :80 with
# the "Welcome to nginx" page. Left enabled, the node's bare address advertises
# the web server to anyone who probes it, and a panel-managed site that wants to
# be the default vhost cannot become one. Panel-managed sites bring their own
# vhosts, so retire it.
if [[ "$MODE" != "sync-packaging" && -e /etc/nginx/sites-enabled/default ]]; then
  if [[ -L /etc/nginx/sites-enabled/default ]]; then
    INSTALL_STATE_DIR="/var/lib/nexa-panel/install"
    ensure_directory 0700 "$INSTALL_STATE_DIR" root root
    if [[ ! -f "$INSTALL_STATE_DIR/nginx-default.target" ]]; then
      journal created "$INSTALL_STATE_DIR/nginx-default.target"
      readlink /etc/nginx/sites-enabled/default > "$INSTALL_STATE_DIR/nginx-default.target"
      chmod 0600 "$INSTALL_STATE_DIR/nginx-default.target"
    fi
    log "Disabling the stock Nginx default site (restore target recorded)"
    journal_path /etc/nginx/sites-enabled/default
    rm -f /etc/nginx/sites-enabled/default
    CHANGED=1
  else
    warn "leaving non-symlink /etc/nginx/sites-enabled/default untouched"
  fi
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
ensure_directory 0755 /etc/ssh/sshd_config.d
if [[ "$MODE" != "sync-packaging" ]] && ! grep -qsE '^\s*Include\s+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config; then
  journal_path /etc/ssh/sshd_config
  printf '\n# Added by Nexa Panel: honour per-site SFTP drop-ins.\nInclude /etc/ssh/sshd_config.d/*.conf\n' >> /etc/ssh/sshd_config
fi
# Generate any missing host keys so `sshd -t` can load them; in an image build
# the package postinst may not have run ssh-keygen. Idempotent: -A only creates
# keys that are absent.
if [[ "$MODE" != "sync-packaging" ]]; then
  ssh-keygen -A
fi
# `sshd -t` needs the privilege-separation directory to exist. On a real host
# tmpfiles/ssh.service create it at boot, but during an image build /run is empty
# and the check would abort with "Missing privilege separation directory".
ensure_directory 0755 /run/sshd
# Validate before enabling so a pre-existing broken config surfaces here, not on
# the first site that enables SFTP.
sshd -t

# --- install binary ---------------------------------------------------------
# Keep the old live binary until every package, managed file, Nginx, and SSH
# validation has succeeded. Self-update owns transactional in-place upgrades;
# this late swap keeps a fresh install failure from exposing an unstartable
# binary early in the process.
if [[ -n "$CANDIDATE_BINARY" ]]; then
  log "Installing the validated candidate as /usr/bin/nexa"
  journal_path /usr/bin/nexa
  install -m 0755 "$CANDIDATE_BINARY" /usr/bin/.nexa.new
  sync -f /usr/bin/.nexa.new 2>/dev/null || true
  mv -fT -- /usr/bin/.nexa.new /usr/bin/nexa
  sync -f /usr/bin 2>/dev/null || true
  CHANGED=1
fi

# --- services ---------------------------------------------------------------
log "Enabling services"
if [[ "$MODE" != "sync-packaging" ]]; then
  journal_unit_enablement "${ENABLED_UNITS[@]}"
  systemctl enable "${ENABLED_UNITS[@]}"
fi

# A packaging refresh stops here: it owns the packaging and nothing else — no
# database, no administrator, no firewall, no certificate — and restarts only
# what a real change requires, so it is safe to run repeatedly on a live node.
if [[ "$MODE" == "sync-packaging" ]]; then
  journal_unit_enablement nexa-update-recovery.service
  systemctl enable nexa-update-recovery.service
  if [[ "$START" -eq 0 ]]; then
    if [[ -d /run/systemd/system ]]; then
      systemctl daemon-reload
    fi
    log "Packaging refreshed and systemd reloaded. Service activation remains pending (--no-start)."
  elif [[ ! -d /run/systemd/system ]]; then
    log "Packaging refreshed. systemd is not running here, so the services pick it up on first boot."
  elif [[ "$CHANGED" -eq 1 ]]; then
    log "Packaging changed; reloading systemd and restarting the panel services"
    systemctl daemon-reload
    journal_service_state nexa-agent.service nexa-api.service nginx.service
    systemctl restart nexa-agent.service nexa-api.service
    if [[ "$NGINX_HARD_RESTART" -eq 1 ]]; then
      systemctl restart nginx.service
    else
      systemctl reload nginx.service || systemctl restart nginx.service
    fi
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
  journal_service_state nexa-agent.service nexa-api.service nginx.service nexa-panel-system-backup.timer
  systemctl restart nexa-agent.service nexa-api.service
  if [[ "$NGINX_HARD_RESTART" -eq 1 ]]; then
    systemctl restart nginx.service
  elif systemctl is-active --quiet nginx.service; then
    systemctl reload nginx.service
  else
    systemctl start nginx.service
  fi
  systemctl start nexa-panel-system-backup.timer
  # Firewall changes are opt-in and reconcile only rules the panel recorded or
  # tagged itself. Do this before Certbot: HTTP-01 cannot succeed through an
  # active firewall whose port 80 rule is still waiting later in the script.
  # UFW is never enabled here, so the installer never guesses an SSH policy.
  if [[ "$MANAGE_FIREWALL" -eq 1 ]] && command -v ufw >/dev/null 2>&1; then
    if ufw status 2>/dev/null | grep -q '^Status: active'; then
      UFW_PORTS=()
      mapfile -t UFW_PORTS < <(panel_firewall_ports)
      UFW_RULES_FILE="/var/lib/nexa-panel/install/ufw.rules"
      ensure_directory 0700 "$(dirname "$UFW_RULES_FILE")" root root
      [[ -e "$UFW_RULES_FILE" ]] || journal created "$UFW_RULES_FILE"
      touch "$UFW_RULES_FILE"
      chmod 0600 "$UFW_RULES_FILE"
      mapfile -t OWNED_UFW_PORTS < <(
        {
          grep -E '^[0-9]{1,5}/(tcp|udp)$' "$UFW_RULES_FILE" 2>/dev/null || true
          ufw status 2>/dev/null | awk '/# Nexa Panel managed/ {print $1}' | grep -E '^[0-9]{1,5}/(tcp|udp)$' || true
        } | sort -u
      )
      log "Reconciling panel-owned UFW rules: ${UFW_PORTS[*]:-(none)}"
      NEXT_OWNED_UFW_PORTS=()
      for ufw_port in "${UFW_PORTS[@]}"; do
        if printf '%s\n' "${OWNED_UFW_PORTS[@]}" | grep -Fxq "$ufw_port"; then
          if ! ufw status 2>/dev/null | grep -Eq "^[[:space:]]*${ufw_port//\//\\/}[[:space:]]+ALLOW([[:space:]]|$)"; then
            journal ufw_added "$ufw_port"
            ufw allow "$ufw_port" comment 'Nexa Panel managed'
          fi
          NEXT_OWNED_UFW_PORTS+=("$ufw_port")
        elif ufw status 2>/dev/null | grep -Eq "^[[:space:]]*${ufw_port//\//\\/}[[:space:]]+ALLOW([[:space:]]|$)"; then
          log "Leaving the pre-existing operator-owned UFW rule unchanged: $ufw_port"
        else
          journal ufw_added "$ufw_port"
          ufw allow "$ufw_port" comment 'Nexa Panel managed'
          NEXT_OWNED_UFW_PORTS+=("$ufw_port")
        fi
      done
      for ufw_port in "${OWNED_UFW_PORTS[@]}"; do
        if printf '%s\n' "${UFW_PORTS[@]}" | grep -Fxq "$ufw_port"; then
          continue
        fi
        if ufw status 2>/dev/null | grep -Eq "^[[:space:]]*${ufw_port//\//\\/}[[:space:]]+ALLOW([[:space:]]|$)"; then
          journal ufw_deleted "$ufw_port"
          ufw --force delete allow "$ufw_port"
        fi
      done
      : > "$WORK_DIR/ufw.rules"
      if (( ${#NEXT_OWNED_UFW_PORTS[@]} > 0 )); then
        printf '%s\n' "${NEXT_OWNED_UFW_PORTS[@]}" > "$WORK_DIR/ufw.rules"
      fi
      install_managed "$WORK_DIR/ufw.rules" 0600 "$UFW_RULES_FILE" root root
    else
      warn "--manage-firewall was supplied, but UFW is inactive; leaving it inactive and unchanged"
    fi
  fi

  if [[ -n "$TLS_EMAIL" ]]; then
    log "Obtaining a TLS certificate for $PANEL_HOSTNAME"
    # Certbot rewrites the vhost in place; the pre-Certbot copy is journalled so
    # a later failure restores the listener this run actually wrote.
    journal_path "$PANEL_VHOST"
    journal certificate "$PANEL_HOSTNAME"
    certbot --nginx --non-interactive --agree-tos --redirect --email "$TLS_EMAIL" -d "$PANEL_HOSTNAME"
  fi

  # Nothing beyond this point may report success on a panel nobody can open, so
  # the published listener is fetched before the administrator is seeded — an
  # unreachable node is a failed install, and the operator should hear that
  # rather than a set of credentials for a panel that does not answer.
  verify_public_ingress "$(planned_panel_url)"

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
    if [[ "$TLS_ACTIVE" -eq 1 ]]; then
      panel_url="https://${PANEL_HOSTNAME}/"
    else
      panel_url="http://${PANEL_HOSTNAME}/"
    fi
  elif [[ "$INSECURE_HTTP" -eq 1 ]]; then
    panel_url="http://${panel_ip}:8888/"
  else
    panel_url="http://localhost:8888/ (use: ssh -L 8888:localhost:8888 <server>)"
  fi
  if [[ -f "$SEED_SCRIPT" ]]; then
    # Straight to the console, never through the transcript: the helper prints the
    # generated administrator password, and a password written into a log file
    # outlives the install and every reason anyone had to read it.
    bash "$SEED_SCRIPT" "$panel_url" >&8 2>&8
  else
    warn "no seed helper at $SEED_SCRIPT; create the first administrator with the bootstrap token in /var/lib/nexa-panel/bootstrap.token"
    log "Nexa Panel is available at ${panel_url}"
  fi
fi
