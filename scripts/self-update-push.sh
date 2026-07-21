#!/usr/bin/env bash
# Push a built Nexa Panel binary to a host and self-update it in place.
#
# This productizes the local-file update flow: copy a binary to the target over
# SSH, then trigger `nexa self-update --binary` on the host so the privileged
# agent validates it, atomically swaps /usr/bin/nexa, and restarts the services.
# No release hosting or download is involved — the operator's own build is the
# source of truth.
#
#   scripts/self-update-push.sh root@example.com
#   scripts/self-update-push.sh --build --arch amd64 deploy@host
#   scripts/self-update-push.sh --binary dist/nexa-linux-arm64 myhost
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET=""
BINARY=""
ARCH=""
BUILD=0
REMOTE_TMP="/tmp/nexa-update-$$"
SOCKET="/run/nexa-panel/agent.sock"
TOKEN="/etc/nexa-panel/agent.token"
SUDO="sudo "
SSH_OPTS_STR=""

log()  { echo "==> $*"; }
die()  { echo "error: $*" >&2; exit 1; }
die_early() { echo "error: $*" >&2; exit 2; }

usage() {
  cat <<'EOF'
Usage: self-update-push.sh [options] <ssh-target>

  <ssh-target>          SSH destination the binary is pushed to, e.g. root@host.

Options:
  --binary PATH         Local prebuilt binary to push. Defaults to
                        dist/nexa-linux-<arch> for the target's architecture.
  --build               Build the release binary first with
                        scripts/build-linux-release.sh <arch> (implies its
                        full `make check`). Mutually exclusive with --binary.
  --arch amd64|arm64    Target architecture. Autodetected over SSH when omitted.
  --remote-tmp PATH     Remote staging path for the upload (default: a unique
                        /tmp path removed after the update).
  --socket PATH         Agent socket on the host (default: /run/nexa-panel/agent.sock).
  --token PATH          Agent token on the host (default: /etc/nexa-panel/agent.token).
  --no-sudo             Run `nexa self-update` without sudo (use when the SSH
                        user is root or in the nexa group).
  --ssh-opts "..."      Extra options passed to ssh and scp (single string).
  -h, --help            Show this message.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BINARY="${2:-}"; [[ -n "$BINARY" ]] || { echo "error: --binary needs a path" >&2; exit 2; }; shift 2 ;;
    --build) BUILD=1; shift ;;
    --arch) ARCH="${2:-}"; [[ -n "$ARCH" ]] || { echo "error: --arch needs a value" >&2; exit 2; }; shift 2 ;;
    --remote-tmp) REMOTE_TMP="${2:-}"; [[ -n "$REMOTE_TMP" ]] || { echo "error: --remote-tmp needs a path" >&2; exit 2; }; shift 2 ;;
    --socket) SOCKET="${2:-}"; [[ -n "$SOCKET" ]] || { echo "error: --socket needs a path" >&2; exit 2; }; shift 2 ;;
    --token) TOKEN="${2:-}"; [[ -n "$TOKEN" ]] || { echo "error: --token needs a path" >&2; exit 2; }; shift 2 ;;
    --no-sudo) SUDO=""; shift ;;
    --ssh-opts) SSH_OPTS_STR="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    -*) echo "error: unknown option: $1" >&2; usage >&2; exit 2 ;;
    *) [[ -z "$TARGET" ]] || die_early "unexpected extra argument: $1"; TARGET="$1"; shift ;;
  esac
done

[[ -n "$TARGET" ]] || { usage >&2; die "an SSH target is required"; }
[[ "$BUILD" -eq 0 || -z "$BINARY" ]] || die "--build and --binary are mutually exclusive"
case "${ARCH:-amd64}" in amd64|arm64) ;; *) die "unsupported architecture: $ARCH (expected amd64 or arm64)" ;; esac

if [[ -n "$SSH_OPTS_STR" ]]; then
  read -ra SSH_OPTS <<< "$SSH_OPTS_STR"
else
  SSH_OPTS=()
fi

# Resolve the target architecture over SSH unless it is fixed by an explicit
# --binary (whose arch the operator has already chosen) or a given --arch.
if [[ -z "$ARCH" && ( -z "$BINARY" || "$BUILD" -eq 1 ) ]]; then
  log "Detecting the architecture of $TARGET"
  remote_machine="$(ssh "${SSH_OPTS[@]+"${SSH_OPTS[@]}"}" "$TARGET" uname -m)" || die "could not reach $TARGET to detect its architecture"
  case "$remote_machine" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "unsupported remote architecture: $remote_machine" ;;
  esac
  log "Target architecture: $ARCH"
fi

# Resolve the binary to push.
if [[ -z "$BINARY" ]]; then
  if [[ "$BUILD" -eq 1 ]]; then
    log "Building the $ARCH release binary"
    bash "$ROOT_DIR/scripts/build-linux-release.sh" "$ARCH"
  fi
  BINARY="$ROOT_DIR/dist/nexa-linux-$ARCH"
fi
[[ -f "$BINARY" ]] || die "no binary at $BINARY (build one with 'scripts/build-linux-release.sh ${ARCH:-<arch>}', pass --binary, or use --build)"

# Upload, then trigger the in-place update. The staged file is always removed,
# whether or not the update succeeds; the update itself returns before the
# services restart, so a clean exit here means the swap was accepted.
log "Uploading $(basename "$BINARY") to $TARGET:$REMOTE_TMP"
scp "${SSH_OPTS[@]+"${SSH_OPTS[@]}"}" "$BINARY" "$TARGET:$REMOTE_TMP"

q_tmp="$(printf '%q' "$REMOTE_TMP")"
q_socket="$(printf '%q' "$SOCKET")"
q_token="$(printf '%q' "$TOKEN")"
remote_cmd="chmod 0755 $q_tmp; ${SUDO}nexa self-update --binary $q_tmp --socket $q_socket --token $q_token; status=\$?; rm -f $q_tmp; exit \$status"

log "Triggering self-update on $TARGET"
# remote_cmd is intentionally assembled (and %q-quoted) here to run on the host;
# the only $-references left for the remote shell are the escaped exit status.
# shellcheck disable=SC2029
ssh "${SSH_OPTS[@]+"${SSH_OPTS[@]}"}" "$TARGET" "$remote_cmd"

log "Done. $TARGET will restart the panel services momentarily to finish the update."
