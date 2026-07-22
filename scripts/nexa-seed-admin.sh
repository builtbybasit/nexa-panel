#!/usr/bin/env bash
# Wait for the API, create the first administrator if none exists, and print its
# credentials. Shared by scripts/install.sh and the disposable Docker node's boot
# unit, so the two never drift.
#
# Seeding goes over the API's local Unix socket, which is the trusted-proxy path,
# and declares the caller's real address — 127.0.0.1, because this script runs on
# the node itself. That is what satisfies both gates in front of the bootstrap
# endpoint: the secure-transport guard (loopback is exempt from the TLS
# requirement) and the first-run gate (loopback is the proof of local access).
# It therefore works on a TLS-fronted node as well as a plaintext one — which
# matters, because a remote browser can NOT complete first-run setup on a
# published hostname: the endpoint demands a bootstrap token the SPA has no field
# for. Idempotent: an existing administrator (409) is left unchanged. Always
# exits 0 on a soft failure so it never blocks a boot.
#
#   nexa-seed-admin.sh [PANEL_URL]
#
set -euo pipefail

PANEL_URL="${1:-}"
SOCKET="/run/nexa-panel/api.sock"
TOKEN_PATH="/var/lib/nexa-panel/bootstrap.token"
# Where the credentials are always written, root-only. stdout is not a safe
# place for a password on its own: when this runs as a systemd unit, everything
# it prints is retained in the journal indefinitely.
CREDENTIALS_PATH="${NEXA_SEED_CREDENTIALS_FILE:-/root/nexa-panel-first-admin.txt}"

log()  { echo "==> $*"; }
warn() { echo "warning: $*" >&2; }

# The address to print. On most cloud providers every local address is private
# and NAT'd, and the operator cannot open any of them, so a public reflector gets
# asked first. The local fallback reads the routing table rather than
# `hostname -I`, which lists addresses in interface order and so answers with a
# bridge address on any node running Docker or libvirt.
if [[ -z "$PANEL_URL" ]]; then
  ip="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  if [[ ! "$ip" =~ ^[0-9]+(\.[0-9]+){3}$ ]]; then
    ip="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}')"
  fi
  if [[ -z "$ip" ]]; then
    ip="$(ip -o -4 addr show scope global 2>/dev/null |
      awk '$2 !~ /^(docker|br-|veth|virbr|tun|tap)/ { split($4, a, "/"); print a[1]; exit }')"
  fi
  [[ -n "$ip" ]] || ip="<server-ip>"
  PANEL_URL="http://${ip}:8888/"
fi

log "Waiting for the API to become ready"
ready=0
for _ in $(seq 1 60); do
  if curl -sf --unix-socket "$SOCKET" http://localhost/api/v1/health/ready >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "$ready" -ne 1 ]]; then
  warn "the API did not become ready in time; create the administrator from the panel on first visit"
  exit 0
fi

# Read a bounded slice of /dev/urandom and cut the password out of it with
# parameter expansion. Piping urandom into `head -c` instead would leave tr
# writing to a closed pipe, which prints a "Broken pipe" error and (under
# pipefail) fails the script.
random_alnum="$(LC_ALL=C tr -dc 'A-Za-z0-9' < <(head -c 1024 /dev/urandom))"
admin_pass="${random_alnum:0:20}"
boot_token="$(cat "$TOKEN_PATH" 2>/dev/null || true)"
resp_body="$(mktemp)"
http_code="$(curl -s -o "$resp_body" -w '%{http_code}' \
  --unix-socket "$SOCKET" \
  -X POST http://localhost/api/v1/auth/bootstrap \
  -H "X-Nexa-Bootstrap-Token: ${boot_token}" \
  -H 'X-Forwarded-For: 127.0.0.1' \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${admin_pass}\"}" 2>/dev/null || echo 000)"
case "$http_code" in
  201)
    banner="$(
      cat <<EOF

========================================================
  Nexa Panel is ready.

  URL:       ${PANEL_URL}
  Username:  admin
  Password:  ${admin_pass}

  Save this password now - it is not shown again.
========================================================
EOF
    )"
    # The password is always written to a root-only file, and printed only to a
    # terminal. Printing it unconditionally would put it in the journal whenever
    # this runs as a systemd unit, where it stays readable for as long as the
    # logs are retained — long after the password should have been rotated.
    (umask 077; printf '%s\n' "$banner" > "$CREDENTIALS_PATH") 2>/dev/null ||
      warn "could not write the credentials to $CREDENTIALS_PATH"
    if [[ -t 1 ]]; then
      printf '%s\n' "$banner"
    else
      log "Created the administrator 'admin'. The password was written to ${CREDENTIALS_PATH} (root-only) because this output is not a terminal; read it, save it elsewhere, and delete the file."
      log "Nexa Panel is available at ${PANEL_URL}"
    fi
    # Optional hand-off file. The disposable Docker node's entrypoint relays this
    # to the container's real stdout, which is the only way a banner printed by a
    # systemd unit can reach `docker compose logs`.
    if [[ -n "${NEXA_SEED_BANNER_FILE:-}" ]]; then
      mkdir -p "$(dirname "$NEXA_SEED_BANNER_FILE")"
      (umask 077; printf '%s\n' "$banner" > "$NEXA_SEED_BANNER_FILE")
    fi
    ;;
  409)
    log "An administrator already exists; leaving it unchanged."
    log "Nexa Panel is available at ${PANEL_URL}"
    ;;
  *)
    warn "could not create the administrator automatically (HTTP ${http_code}). Detail: $(tr -d '\n' < "$resp_body")"
    # The browser cannot recover from this on its own on a published hostname:
    # the bootstrap endpoint rejects a remote caller that presents no token, and
    # the sign-in page has nowhere to enter one. Hand the operator the exact
    # command that still works, from the node itself.
    warn "create the first administrator by hand, from a shell on this node:"
    warn "  sudo curl --unix-socket ${SOCKET} -X POST http://localhost/api/v1/auth/bootstrap \\"
    warn "    -H \"X-Nexa-Bootstrap-Token: \$(sudo cat ${TOKEN_PATH})\" \\"
    warn "    -H 'X-Forwarded-For: 127.0.0.1' -H 'Content-Type: application/json' \\"
    warn "    -d '{\"username\":\"admin\",\"password\":\"CHOOSE-A-STRONG-PASSWORD\"}'"
    warn "then sign in at ${PANEL_URL}"
    ;;
esac
rm -f "$resp_body"
