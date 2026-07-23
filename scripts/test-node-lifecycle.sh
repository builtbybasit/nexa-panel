#!/usr/bin/env bash
# Executed host lifecycle scenarios for the disposable Ubuntu/systemd node.
#
# scripts/test-node-acceptance.sh checks a freshly installed node. This script
# checks what happens to that node afterwards: re-running the installer, a
# refused install, retain-data uninstall, reinstall on top of retained state,
# and purge uninstall. Every scenario runs the real scripts/install.sh and
# scripts/uninstall.sh against a real systemd host and asserts on the resulting
# filesystem, accounts, units, and API — none of it reads the scripts' source.
#
#   bash scripts/test-node-lifecycle.sh dist/nexa-linux-amd64
#
# The binary is COPYed into an image rather than bind-mounted the way
# compose.yaml mounts it: purge uninstall deletes /usr/bin/nexa, and a bind
# mount cannot be deleted from inside the container. Everything else — the base
# image, the installer, the packaging tree — is exactly what the node ships.
set -euo pipefail

BINARY="${1:?usage: test-node-lifecycle.sh PATH_TO_LINUX_BINARY}"
BASE_IMAGE="${NEXA_LIFECYCLE_BASE_IMAGE:-nexa-node}"
CONTAINER="${NEXA_LIFECYCLE_CONTAINER:-nexa-lifecycle}"
IMAGE="${NEXA_LIFECYCLE_IMAGE:-nexa-lifecycle}"
HOST_PORT="${NEXA_LIFECYCLE_PORT:-8899}"
READY_ATTEMPTS="${NEXA_LIFECYCLE_READY_ATTEMPTS:-120}"
SOURCE_DIR=/opt/nexa-src
CANARY_SITE=/srv/nexa/sites/lifecycle-canary
CANARY_FILE="$CANARY_SITE/public/index.html"
CANARY_CONTENT='lifecycle-customer-data'

[[ -f "$BINARY" ]] || { echo "error: no binary at $BINARY (build one with scripts/build-linux-release.sh)" >&2; exit 1; }

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="$(mktemp -d)"
cleanup() {
  local status=$?
  if [[ "$status" -ne 0 ]]; then
    echo "--- lifecycle failure diagnostics ---" >&2
    docker exec "$CONTAINER" systemctl --failed --no-pager >&2 2>/dev/null || true
    docker exec "$CONTAINER" journalctl -u nexa-agent -u nexa-api -u nginx --no-pager -n 200 >&2 2>/dev/null || true
  fi
  rm -rf -- "$WORK_DIR"
  if [[ "${NEXA_LIFECYCLE_KEEP:-0}" != "1" ]]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT

scenario() { printf '\n==> %s\n' "$*"; }
fail() { printf 'lifecycle failure: %s\n' "$*" >&2; exit 1; }
node() { docker exec "$CONTAINER" "$@"; }
node_sh() { docker exec "$CONTAINER" bash -ceu "$1"; }

# --- node under test --------------------------------------------------------
docker image inspect "$BASE_IMAGE" >/dev/null 2>&1 ||
  fail "no $BASE_IMAGE image; build it first with: docker build -t $BASE_IMAGE ."
cp "$BINARY" "$WORK_DIR/nexa"
printf 'FROM %s\nCOPY --chmod=0755 nexa /usr/bin/nexa\n' "$BASE_IMAGE" > "$WORK_DIR/Dockerfile"
docker build -q -t "$IMAGE" "$WORK_DIR" >/dev/null
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" --privileged --cgroupns=host \
  --dns 8.8.8.8 --dns 1.1.1.1 \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
  -p "$HOST_PORT:8888" "$IMAGE" >/dev/null

wait_ready() {
  for _ in $(seq 1 "$READY_ATTEMPTS"); do
    if node curl -fsS --unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/health/ready >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}
wait_ready || fail "the node never became ready"

# The image deletes the installer it was built with, so give the node back the
# exact same tree (installer, uninstaller, packaging, binary) to re-run from.
node mkdir -p "$SOURCE_DIR/scripts" "$SOURCE_DIR/bin"
docker cp "$REPO_DIR/packaging" "$CONTAINER:$SOURCE_DIR/packaging"
for script in install.sh uninstall.sh nexa-seed-admin.sh nexa-release-helper.py; do
  docker cp "$REPO_DIR/scripts/$script" "$CONTAINER:$SOURCE_DIR/scripts/$script"
done
docker cp "$WORK_DIR/nexa" "$CONTAINER:$SOURCE_DIR/bin/nexa"
node chmod 0755 "$SOURCE_DIR/scripts/install.sh" "$SOURCE_DIR/scripts/uninstall.sh" "$SOURCE_DIR/bin/nexa"

installer() { node "$SOURCE_DIR/scripts/install.sh" "$@"; }

# --- host state snapshots ---------------------------------------------------
# Two snapshots, because they answer different questions. The packaging snapshot
# is byte- and timestamp-exact: install_managed only writes a managed file when
# its content, mode, or owner differs, so a changed mtime there IS drift. The
# state snapshot covers directories the panel writes to at runtime, so it
# records the ownership and permission surface only.
# shellcheck disable=SC2016  # the snapshot program is expanded inside the node, not here
SNAPSHOT_PACKAGING='
shopt -s nullglob
for path in \
  /usr/bin/nexa /usr/sbin/nexa-uninstall /usr/lib/nexa-panel/uninstall.sh \
  /usr/lib/systemd/system/nexa-*.service /usr/lib/systemd/system/nexa-*.timer \
  /etc/systemd/system/nexa-api.service.d/*.conf \
  /usr/lib/sysusers.d/nexa-panel.conf /usr/lib/tmpfiles.d/nexa-panel.conf \
  /etc/nexa-panel/release-signers \
  /etc/nginx/sites-available/nexa-panel.conf /etc/nginx/sites-enabled/nexa-panel.conf \
  /etc/nginx/snippets/nexa-panel-proxy.conf \
  /var/lib/nexa-panel/install/ownership.v1
do
  if [[ -L "$path" ]]; then
    # ln -sfn recreates the sites-enabled link on every run, so its timestamp is
    # not drift; where it points is.
    printf "%s symlink %s -> %s\n" "$path" "$(stat -c "%U:%G" "$path")" "$(readlink "$path")"
  elif [[ -f "$path" ]]; then
    printf "%s file %s %s\n" "$path" "$(stat -c "%U:%G %a %Y" "$path")" "$(sha256sum < "$path" | cut -d" " -f1)"
  else
    printf "%s absent\n" "$path"
  fi
done
printf "account %s\n" "$(getent passwd nexa || echo none)"
printf "group %s\n" "$(getent group nexa || echo none)"
printf "www-data-groups %s\n" "$(id -nG www-data | tr " " "\n" | sort | tr "\n" " ")"
'
# shellcheck disable=SC2016  # expanded inside the node, not here
SNAPSHOT_STATE='
for root in /etc/nexa-panel /var/lib/nexa-panel /var/log/nexa-panel /srv/nexa; do
  if [[ ! -d "$root" ]]; then
    printf "%s absent\n" "$root"
    continue
  fi
  # SQLite side files come and go with the write load, and the installer drops a
  # fresh 0600 transcript under /var/log; neither is state drift.
  find "$root" -name "control.db-wal" -prune -o -name "control.db-shm" -prune -o \
    -name "nexa-panel-install.*.log" -prune -o \
    -printf "%p %u:%g %m %y\n" | sort
done
'
snapshot() {
  local program="$1" destination="$2"
  docker exec "$CONTAINER" bash -ceu "$program" > "$destination"
}
assert_no_drift() {
  local label="$1" before="$2" after="$3"
  if ! diff -u "$before" "$after" > "$WORK_DIR/drift.diff"; then
    cat "$WORK_DIR/drift.diff" >&2
    fail "$label changed the node"
  fi
}
assert_absent() {
  local path
  for path in "$@"; do
    node_sh "[[ ! -e '$path' && ! -L '$path' ]]" || fail "$path still exists"
  done
}
assert_present() {
  local path
  for path in "$@"; do
    node_sh "[[ -e '$path' ]]" || fail "$path is missing"
  done
}

# The seeded administrator proves state and secrets survived: the password is
# only ever verified against the hash in the retained control database, and the
# session it mints is signed with the retained master key.
#
# The credentials file is written by nexa-firstadmin.service, which orders itself
# after nexa-api but is a separate unit — a ready API socket does not mean the
# seeding has finished, so poll rather than read once.
ADMIN_PASSWORD=""
for _ in $(seq 1 "$READY_ATTEMPTS"); do
  ADMIN_PASSWORD="$(node_sh "sed -n 's/^ *Password: *//p' /root/nexa-panel-first-admin.txt 2>/dev/null | head -n 1" || true)"
  [[ -z "$ADMIN_PASSWORD" ]] || break
  sleep 1
done
[[ -n "$ADMIN_PASSWORD" ]] || fail "the node never wrote the seeded administrator credentials"
assert_admin_can_sign_in() {
  local label="$1" code
  code="$(node_sh "curl -s -o /dev/null -w '%{http_code}' --unix-socket /run/nexa-panel/api.sock \
    -X POST http://localhost/api/v1/auth/login \
    -H 'Origin: http://localhost' -H 'X-Forwarded-For: 127.0.0.1' \
    -H 'Content-Type: application/json' \
    -d '{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}'")"
  [[ "$code" == "200" ]] || fail "$label: the seeded administrator could not sign in (HTTP $code)"
}
assert_admin_can_sign_in "baseline"

node_sh "install -d -m 0755 '$CANARY_SITE/public' && printf '%s' '$CANARY_CONTENT' > '$CANARY_FILE'"

# --- scenario 1: idempotent re-run ------------------------------------------
scenario "Scenario 1: re-running the installer on a live node changes nothing"
snapshot "$SNAPSHOT_PACKAGING" "$WORK_DIR/packaging.before"
snapshot "$SNAPSHOT_STATE" "$WORK_DIR/state.before"
# No ingress flags: a flagless re-run must preserve how the node is published
# instead of silently reverting it to the loopback default.
installer --allow-existing > "$WORK_DIR/reinstall.log" 2>&1 ||
  { cat "$WORK_DIR/reinstall.log" >&2; fail "re-running the installer failed"; }
wait_ready || fail "the node did not come back after the installer re-run"
snapshot "$SNAPSHOT_PACKAGING" "$WORK_DIR/packaging.after"
snapshot "$SNAPSHOT_STATE" "$WORK_DIR/state.after"
assert_no_drift "the installer re-run" "$WORK_DIR/packaging.before" "$WORK_DIR/packaging.after"
assert_no_drift "the installer re-run" "$WORK_DIR/state.before" "$WORK_DIR/state.after"
# The node under test was installed with --allow-insecure-http and no hostname,
# so it publishes on every interface: the installer renders a bare `listen 8888;`
# (plus the IPv6 form), never an address-qualified one. The insecure default the
# re-run must NOT fall back to is the loopback bootstrap listener, so assert both
# halves — that the all-interfaces listener is still there, and that the
# loopback one has not appeared.
node_sh "grep -qE '^[[:space:]]*listen[[:space:]]+8888;' /etc/nginx/sites-available/nexa-panel.conf" ||
  fail "the flagless re-run rewrote the published listener"
node_sh "! grep -q '127.0.0.1:8888' /etc/nginx/sites-available/nexa-panel.conf" ||
  fail "the flagless re-run reverted the node to the loopback bootstrap listener"
node_sh "grep -qE '^[[:space:]]*server_name[[:space:]]+_;' /etc/nginx/sites-available/nexa-panel.conf" ||
  fail "the flagless re-run rewrote the published server name"
node_sh "grep -q '^Environment=NEXA_ALLOW_INSECURE_HTTP=1$' /etc/systemd/system/nexa-api.service.d/10-nexa-panel.conf" ||
  fail "the flagless re-run dropped the recorded plaintext publishing decision"
# The publication is managed state, not something the next run has to reconstruct
# from the vhost. `show` reports its source, so a record that was silently
# inferred rather than written fails here.
PUBLISHING_SHOWN="$(node /usr/bin/nexa publishing show)"
grep -q '^Publishing: plaintext$' <<< "$PUBLISHING_SHOWN" ||
  fail "the node does not record its plaintext publication: $PUBLISHING_SHOWN"
grep -q '^  Source:   install' <<< "$PUBLISHING_SHOWN" ||
  fail "the publication is still being inferred rather than recorded: $PUBLISHING_SHOWN"
assert_admin_can_sign_in "after the installer re-run"

# --- scenario 2: refused install ---------------------------------------------
scenario "Scenario 2: a refused install mutates nothing"
snapshot "$SNAPSHOT_PACKAGING" "$WORK_DIR/packaging.before"
snapshot "$SNAPSHOT_STATE" "$WORK_DIR/state.before"
# The refusal is candidate validation, not the preflight: a validated ownership
# marker now implies --allow-existing, precisely so a re-run on a live node
# succeeds, so "already installed" is no longer a blocker to test against. What
# still must be refused — and what bricked a live node when it was not — is an
# artifact that runs and reports a version without being nexa.
node_sh "printf '#!/bin/sh\nexit 0\n' > /tmp/not-nexa && chmod 0755 /tmp/not-nexa"
if installer --binary /tmp/not-nexa > "$WORK_DIR/refused.log" 2>&1; then
  cat "$WORK_DIR/refused.log" >&2
  fail "the installer accepted a candidate that is not a Nexa Panel binary"
fi
grep -q 'not an executable Nexa Panel binary\|carries no embedded web UI' "$WORK_DIR/refused.log" ||
  { cat "$WORK_DIR/refused.log" >&2; fail "the install failed for a reason other than candidate validation"; }
snapshot "$SNAPSHOT_PACKAGING" "$WORK_DIR/packaging.after"
snapshot "$SNAPSHOT_STATE" "$WORK_DIR/state.after"
assert_no_drift "the refused install" "$WORK_DIR/packaging.before" "$WORK_DIR/packaging.after"
assert_no_drift "the refused install" "$WORK_DIR/state.before" "$WORK_DIR/state.after"
node systemctl is-active --quiet nexa-agent.service nexa-api.service nginx.service ||
  fail "the refused install disturbed the running services"
assert_admin_can_sign_in "after the refused install"

# --- scenario 3: retain-data uninstall ---------------------------------------
scenario "Scenario 3: uninstall removes the program and keeps customer data"
node /usr/sbin/nexa-uninstall > "$WORK_DIR/uninstall-retain.log" 2>&1 ||
  { cat "$WORK_DIR/uninstall-retain.log" >&2; fail "the retain-data uninstall failed"; }
assert_absent \
  /usr/bin/nexa \
  /usr/sbin/nexa-uninstall \
  /usr/lib/nexa-panel \
  /usr/lib/systemd/system/nexa-agent.service \
  /usr/lib/systemd/system/nexa-api.service \
  /usr/lib/systemd/system/nexa-panel-system-backup.timer \
  /usr/lib/sysusers.d/nexa-panel.conf \
  /usr/lib/tmpfiles.d/nexa-panel.conf \
  /etc/nginx/sites-enabled/nexa-panel.conf \
  /etc/nginx/sites-available/nexa-panel.conf \
  /etc/systemd/system/nexa-api.service.d
for unit in nexa-agent.service nexa-api.service; do
  [[ "$(node systemctl show --property=LoadState --value "$unit")" == "not-found" ]] ||
    fail "$unit is still known to systemd after uninstall"
done
assert_present \
  /var/lib/nexa-panel/control.db \
  /var/lib/nexa-panel/install/ownership.v1 \
  /etc/nexa-panel/agent.token \
  /etc/nexa-panel/publishing.json \
  "$CANARY_FILE"
[[ "$(node cat "$CANARY_FILE")" == "$CANARY_CONTENT" ]] || fail "hosted site data was altered by uninstall"
node_sh "getent passwd nexa >/dev/null" || fail "the retain-data uninstall deleted the service account"
node nginx -t >/dev/null 2>&1 || fail "Nginx configuration is invalid after uninstall"
node systemctl is-active --quiet nginx.service || fail "uninstall stopped the host web server"

# --- scenario 4: reinstall over retained state -------------------------------
scenario "Scenario 4: reinstalling recovers the retained state and secrets"
# Deliberately flagless. The uninstall above removed the panel vhost and the API
# drop-in, which used to be the only two places this node's publication existed;
# a reinstall that had to re-derive it from them saw a fresh machine and
# republished on loopback. The retained record is now the only thing that can
# restore the all-interfaces plaintext listener this node was installed with.
installer --binary "$SOURCE_DIR/bin/nexa" > "$WORK_DIR/reinstall-after-retain.log" 2>&1 ||
  { tail -50 "$WORK_DIR/reinstall-after-retain.log" >&2; fail "reinstalling over retained state failed"; }
wait_ready || fail "the reinstalled node never became ready"
assert_present /usr/bin/nexa /usr/sbin/nexa-uninstall /etc/nginx/sites-enabled/nexa-panel.conf
node_sh "grep -qE '^[[:space:]]*listen[[:space:]]+8888;' /etc/nginx/sites-available/nexa-panel.conf" ||
  fail "the reinstall did not restore the recorded all-interfaces listener"
node_sh "! grep -q '127.0.0.1:8888' /etc/nginx/sites-available/nexa-panel.conf" ||
  fail "the reinstall silently downgraded the node to the loopback bootstrap listener"
node_sh "grep -q '^Environment=NEXA_ALLOW_INSECURE_HTTP=1$' /etc/systemd/system/nexa-api.service.d/10-nexa-panel.conf" ||
  fail "the reinstall dropped the recorded cleartext publishing decision"
node systemctl is-active --quiet nexa-agent.service nexa-api.service nginx.service ||
  fail "the reinstalled services are not running"
# The password was never re-seeded: the installer's seed helper finds an
# existing administrator and leaves it alone, so a successful sign-in proves the
# retained control database and its encryption key both came back.
assert_admin_can_sign_in "after reinstalling over retained state"
[[ "$(node cat "$CANARY_FILE")" == "$CANARY_CONTENT" ]] || fail "hosted site data was altered by the reinstall"

# --- scenario 5: purge uninstall ---------------------------------------------
scenario "Scenario 5: purge uninstall leaves nothing the panel owned"
node /usr/sbin/nexa-uninstall --purge-data --yes > "$WORK_DIR/uninstall-purge.log" 2>&1 ||
  { cat "$WORK_DIR/uninstall-purge.log" >&2; fail "the purge uninstall failed"; }
assert_absent \
  /usr/bin/nexa \
  /usr/sbin/nexa-uninstall \
  /usr/lib/nexa-panel \
  /etc/nexa-panel \
  /var/lib/nexa-panel \
  /var/lib/nexa-panel-update \
  /var/log/nexa-panel \
  /srv/nexa \
  /run/nexa-panel \
  /etc/nginx/sites-available/nexa-panel.conf \
  /etc/nginx/sites-enabled/nexa-panel.conf \
  /etc/nginx/snippets/nexa-panel-proxy.conf \
  /usr/lib/sysusers.d/nexa-panel.conf \
  /usr/lib/tmpfiles.d/nexa-panel.conf
node_sh "! getent passwd nexa >/dev/null" || fail "the nexa service account survived the purge"
node_sh "! getent group nexa >/dev/null" || fail "the nexa service group survived the purge"
# Every remaining nexa* unit, not just the ones this script happens to name, so
# a unit added to packaging without a matching uninstall entry fails here.
# nexa-firstadmin.service is excluded because the panel does not install it: the
# Dockerfile creates it to print the seeded administrator on the disposable
# node's console, and nothing in the ownership manifest claims it.
leftovers="$(node_sh "systemctl list-unit-files --no-legend --plain 'nexa*' | awk '{print \$1}' | grep -vx 'nexa-firstadmin.service' || true")"
[[ -z "$leftovers" ]] || fail "systemd still knows panel units after the purge: $leftovers"
node nginx -t >/dev/null 2>&1 || fail "Nginx configuration is invalid after the purge"
node systemctl is-active --quiet nginx.service || fail "the purge stopped the host web server"

printf '\nNexa Panel lifecycle scenarios passed for %s\n' "$CONTAINER"
