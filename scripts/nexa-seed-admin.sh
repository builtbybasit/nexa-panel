#!/usr/bin/env bash
# Wait for the API, create the first administrator if none exists, and print its
# credentials. Shared by scripts/install.sh (quick-start) and the disposable
# Docker node's boot unit, so the two never drift.
#
# Only works when the panel serves a plaintext mode: seeding goes over the API's
# local Unix socket, which is not TLS-fronted, so the secure-transport guard
# admits it only when NEXA_ALLOW_INSECURE_HTTP is set. Idempotent: an existing
# administrator (409) is left unchanged. Always exits 0 on a soft failure so it
# never blocks a boot.
#
#   nexa-seed-admin.sh [PANEL_URL]
#
set -euo pipefail

PANEL_URL="${1:-}"
SOCKET="/run/nexa-panel/api.sock"
TOKEN_PATH="/var/lib/nexa-panel/bootstrap.token"

log()  { echo "==> $*"; }
warn() { echo "warning: $*" >&2; }

# Fall back to the primary IP if no display URL was supplied.
if [[ -z "$PANEL_URL" ]]; then
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
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
    printf '%s\n' "$banner"
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
    ;;
  *)
    warn "could not create the administrator automatically (HTTP ${http_code}); create it from the panel. Detail: $(tr -d '\n' < "$resp_body")"
    ;;
esac
rm -f "$resp_body"
