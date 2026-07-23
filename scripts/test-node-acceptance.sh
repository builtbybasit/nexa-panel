#!/usr/bin/env bash
# Acceptance checks for the disposable Ubuntu/systemd node.
set -euo pipefail

CONTAINER="${1:-nexa-node}"
BASE_URL="${NEXA_TEST_BASE_URL:-http://localhost:8888}"
READY_ATTEMPTS="${NEXA_TEST_READY_ATTEMPTS:-90}"

docker_exec() {
  docker exec "$CONTAINER" "$@"
}

ready=0
for _ in $(seq 1 "$READY_ATTEMPTS"); do
  if curl -fsS "$BASE_URL/api/v1/health/ready" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "$ready" -ne 1 ]]; then
  echo "node did not become ready: $BASE_URL" >&2
  docker logs --tail 200 "$CONTAINER" >&2 || true
  exit 1
fi

docker_exec systemctl is-active --quiet nexa-agent.service nexa-api.service nginx.service
if failed="$(docker_exec systemctl --failed --no-legend --plain)" && [[ -n "$failed" ]]; then
  printf 'failed units:\n%s\n' "$failed" >&2
  exit 1
fi
docker_exec nginx -t
docker_exec curl -fsS --unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/health/live >/dev/null
docker_exec curl -fsS --unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/health/ready >/dev/null
curl -fsS "$BASE_URL/api/v1/health/live" >/dev/null

# The panel's exporter emits TYPE lines and samples, not HELP text, so assert on
# what it actually publishes: a typed metric and a sample carrying this build's
# version. A bare "some # comment came back" check would pass on an error page
# that happened to start with a hash.
metrics="$(docker_exec curl -fsS http://localhost:8888/metrics)"
grep -q '^# TYPE nexa_build_info gauge$' <<< "$metrics" || {
  echo "metrics endpoint did not return Prometheus exposition" >&2
  printf '%s\n' "$metrics" | head -5 >&2
  exit 1
}
grep -qE '^nexa_build_info\{version="[^"]+"\} 1$' <<< "$metrics" || {
  echo "metrics endpoint published no build-info sample" >&2
  exit 1
}
grep -q '^# TYPE nexa_http_requests_total counter$' <<< "$metrics" || {
  echo "metrics endpoint published no HTTP request counter" >&2
  exit 1
}

if docker_exec id -nG www-data | tr ' ' '\n' | grep -qx nexa; then
  echo "www-data still belongs to the privileged nexa group" >&2
  exit 1
fi
# shellcheck disable=SC2016  # the program is expanded inside the node, not here
docker_exec bash -ceu '
  privileged_gid="$(getent group nexa | cut -d: -f3)"
  web_uid="$(id -u www-data)"
  workers=0
  for status in /proc/[0-9]*/status; do
    [[ -r "$status" ]] || continue
    read -r _ process_uid _ < <(grep "^Uid:" "$status")
    [[ "$process_uid" == "$web_uid" ]] || continue
    process_name="$(sed -n "s/^Name:[[:space:]]*//p" "$status")"
    [[ "$process_name" == "nginx" ]] || continue
    workers=$((workers + 1))
    if sed -n "s/^Groups:[[:space:]]*//p" "$status" | tr " " "\n" | grep -Fxq "$privileged_gid"; then
      echo "running Nginx worker $(basename "$(dirname "$status")") retains privileged nexa gid $privileged_gid" >&2
      exit 1
    fi
  done
  (( workers > 0 )) || { echo "no www-data Nginx worker was found" >&2; exit 1; }
'
if docker_exec runuser -u www-data -- test -r /etc/nexa-panel/agent.token; then
  echo "www-data can read the privileged agent credential" >&2
  exit 1
fi
if docker_exec runuser -u www-data -- test -w /run/nexa-panel/agent.sock; then
  echo "www-data can connect to the privileged agent socket" >&2
  exit 1
fi

docker_exec nexa doctor --preflight --allow-existing --json >/dev/null

# Captured once, then matched: piping straight into `grep -q` makes grep exit at
# the first match, which SIGPIPEs the writer and fails the pipeline under
# `pipefail` — the check would report a broken uninstall plan on a good node.
uninstall_plan="$(docker_exec nexa-uninstall --dry-run)"
for retained in '/var/lib/nexa-panel' '/srv/nexa/sites'; do
  grep -qx "RETAIN $retained" <<< "$uninstall_plan" || {
    echo "the default uninstall plan does not retain $retained" >&2
    exit 1
  }
done

echo "Nexa Panel acceptance checks passed for $CONTAINER ($BASE_URL)"
