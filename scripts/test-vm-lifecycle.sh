#!/usr/bin/env bash
# Lifecycle scenarios that a container cannot prove, run on a throwaway VM.
#
# scripts/test-node-lifecycle.sh covers everything the disposable Docker node
# can honestly answer. The five scenarios here cannot run there, and are NOT
# simulated in CI:
#
#   fresh-tls         needs a public DNS name and inbound :80/:443 so Let's
#                     Encrypt can validate and issue a REAL certificate.
#   reboot            needs a machine that can actually reboot; a container's
#                     PID 1 restart is not a host boot.
#   update            needs two signed releases (N-1 and N) and a writable
#                     /usr/bin/nexa; the node image bind-mounts it read-only.
#   update-failure    needs the activation path to fail on a live host and the
#                     operator to undo it — including the systemd unit graph.
#   offline-rollback  needs recovery to work with nexa-api and nexa-agent
#                     stopped, which is only meaningful on a real host.
#
# This script is the entry point an operator or a VM runner invokes. It is
# deliberately not wired into .github/workflows/ci.yml: GitHub-hosted runners
# provide none of the above. docs/ci-lifecycle.md records what is required.
#
# Run the whole matrix by hand on a fresh, throwaway Ubuntu 24.04 server:
#
#   sudo bash scripts/test-vm-lifecycle.sh all \
#     --hostname panel.example.com --tls-email ops@example.com \
#     --previous /root/nexa-n-1 --target /root/nexa-n
#   # ... the machine reboots at the end; reconnect, then:
#   sudo bash scripts/test-vm-lifecycle.sh all --resume
#
# `all` prints a numbered PASS/FAIL line per scenario and exits nonzero if any
# scenario failed. The reboot is the last scenario precisely so the run needs
# only one reconnect: a script cannot outlive the reboot it requests.
#
# Individual scenarios remain separately runnable, in this order (the chain is
# stateful: offline-rollback needs the transaction `update` wrote, and
# update-failure ends it in a failed state):
#
#   sudo bash scripts/test-vm-lifecycle.sh fresh-tls \
#     --hostname panel.example.com --tls-email ops@example.com \
#     --binary /root/nexa-n-1
#   sudo bash scripts/test-vm-lifecycle.sh update \
#     --previous /root/nexa-n-1 --target /root/nexa-n
#   sudo bash scripts/test-vm-lifecycle.sh offline-rollback
#   sudo bash scripts/test-vm-lifecycle.sh update-failure
#   sudo bash scripts/test-vm-lifecycle.sh reboot --arm
#   # ... machine reboots; the runner reconnects ...
#   sudo bash scripts/test-vm-lifecycle.sh reboot --verify
#
# Every scenario destroys the machine it runs on: it installs, updates, rolls
# back, and reboots the host, and it seeds a site under /srv/nexa. Never point
# it at a host that serves anything. `all` refuses to start on a machine that
# already has a panel unless --yes acknowledges it.
set -euo pipefail

SCENARIO="${1:-}"
shift || true
# `--help` with no scenario in front of it is the first thing an operator types.
case "$SCENARIO" in -h|--help) SCENARIO=help ;; esac

HOSTNAME_ARG=""
TLS_EMAIL=""
BINARY=""
PREVIOUS=""
TARGET=""
PHASE=""
ASSUME_YES=0
SOURCE_DIR="${NEXA_VM_SOURCE_DIR:-/opt/nexa-src}"
STATE_DIR=/var/lib/nexa-panel-vm-lifecycle
TRANSACTION=/var/lib/nexa-panel-update/transaction.json
CANARY=/srv/nexa/sites/vm-canary/public/index.html
CANARY_CONTENT='vm-lifecycle-customer-data'
READY_ATTEMPTS="${NEXA_VM_READY_ATTEMPTS:-120}"

log()  { printf '\n==> %s\n' "$*"; }
fail() { printf 'vm lifecycle failure: %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '/^# Lifecycle scenarios/,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" |
    sed -e '$d' -e 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --hostname)    HOSTNAME_ARG="${2:?--hostname needs a value}"; shift 2 ;;
    --tls-email)   TLS_EMAIL="${2:?--tls-email needs a value}"; shift 2 ;;
    --binary)      BINARY="${2:?--binary needs a value}"; shift 2 ;;
    --previous)    PREVIOUS="${2:?--previous needs a value}"; shift 2 ;;
    --target)      TARGET="${2:?--target needs a value}"; shift 2 ;;
    --arm)         PHASE=arm; shift ;;
    --verify)      PHASE=verify; shift ;;
    --resume)      PHASE=resume; shift ;;
    --yes)         ASSUME_YES=1; shift ;;
    -h|--help)     usage; exit 0 ;;
    *)             fail "unknown option $1" ;;
  esac
done

[[ -n "$SCENARIO" ]] || { usage; exit 2; }
[[ "$SCENARIO" != "help" ]] || { usage; exit 0; }
[[ "$(id -u)" -eq 0 ]] || fail "every scenario mutates the host and must run as root"

installer() {
  [[ -x "$SOURCE_DIR/scripts/install.sh" ]] ||
    fail "no installer at $SOURCE_DIR/scripts/install.sh; unpack the release tree there first"
  "$SOURCE_DIR/scripts/install.sh" "$@"
}

wait_ready() {
  for _ in $(seq 1 "$READY_ATTEMPTS"); do
    if curl -fsS --unix-socket /run/nexa-panel/api.sock \
      http://localhost/api/v1/health/ready >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# The seeded administrator is the only proof that state AND secrets survived: the
# password is verified against the hash in the control database, and the session
# it mints is signed with the master key held beside it.
admin_password() {
  sed -n 's/^ *Password: *//p' /root/nexa-panel-first-admin.txt | head -n 1
}

assert_admin_can_sign_in() {
  local label="$1" base="${2:-}" password code curl_args=()
  password="$(admin_password)"
  [[ -n "$password" ]] || fail "$label: no seeded administrator credentials on this host"
  if [[ -n "$base" ]]; then
    curl_args=("$base/api/v1/auth/login" -H "Origin: $base")
  else
    curl_args=(--unix-socket /run/nexa-panel/api.sock http://localhost/api/v1/auth/login
      -H 'Origin: http://localhost' -H 'X-Forwarded-For: 127.0.0.1')
  fi
  code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "${curl_args[@]}" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$password\"}")"
  [[ "$code" == "200" ]] || fail "$label: the seeded administrator could not sign in (HTTP $code)"
}

assert_services_healthy() {
  local label="$1" failed
  systemctl is-active --quiet nexa-agent.service nexa-api.service nginx.service ||
    fail "$label: the panel services are not all running"
  failed="$(systemctl --failed --no-legend --plain)"
  [[ -z "$failed" ]] || fail "$label: systemd reports failed units: $failed"
  wait_ready || fail "$label: the panel never reported ready"
}

installed_version() { /usr/bin/nexa version | head -n 1; }
binary_digest()     { sha256sum /usr/bin/nexa | cut -d' ' -f1; }

# The managed-packaging fingerprint. A "complete" rollback means the unit graph
# and proxy configuration came back too, not just the executable, so the update
# scenarios diff this either side of the transaction.
packaging_digest() {
  local path
  for path in /usr/bin/nexa /usr/sbin/nexa-uninstall \
    /usr/lib/systemd/system/nexa-agent.service /usr/lib/systemd/system/nexa-api.service \
    /usr/lib/sysusers.d/nexa-panel.conf /usr/lib/tmpfiles.d/nexa-panel.conf \
    /etc/nginx/sites-available/nexa-panel.conf /etc/nginx/snippets/nexa-panel-proxy.conf
  do
    if [[ -f "$path" ]]; then
      printf '%s %s %s\n' "$path" "$(stat -c '%U:%G %a' "$path")" \
        "$(sha256sum < "$path" | cut -d' ' -f1)"
    else
      printf '%s absent\n' "$path"
    fi
  done
}

transaction_field() {
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2], ""))' \
    "$TRANSACTION" "$1"
}

seed_canary() {
  install -d -m 0755 "$(dirname "$CANARY")"
  printf '%s' "$CANARY_CONTENT" > "$CANARY"
}

assert_canary_intact() {
  [[ -f "$CANARY" && "$(cat "$CANARY")" == "$CANARY_CONTENT" ]] ||
    fail "$1: hosted site data did not survive"
}

# --- fresh TLS install -------------------------------------------------------
# Assertions required by the release gate: preflight, service AND public
# readiness, administrator creation. The reboot assertion is the `reboot`
# scenario below, because a script cannot outlive the reboot it requests.
scenario_fresh_tls() {
  [[ -n "$HOSTNAME_ARG" ]] || fail "fresh-tls needs --hostname (a public DNS name pointing at this VM)"
  [[ -n "$TLS_EMAIL" ]]    || fail "fresh-tls needs --tls-email for the ACME account"
  [[ ! -e /usr/bin/nexa ]] || fail "fresh-tls must start from a clean machine; /usr/bin/nexa already exists"

  log "Preflight on a clean machine"
  local binary_args=()
  if [[ -n "$BINARY" ]]; then
    binary_args=(--binary "$BINARY")
  fi

  log "Installing with a real Let's Encrypt certificate for $HOSTNAME_ARG"
  installer --panel-hostname "$HOSTNAME_ARG" --tls-email "$TLS_EMAIL" "${binary_args[@]}"

  log "Asserting the certificate is real and issued by a public CA"
  local chain="/etc/letsencrypt/live/$HOSTNAME_ARG/fullchain.pem"
  [[ -s "$chain" ]] || fail "no certificate chain at $chain"
  local issuer subject
  issuer="$(openssl x509 -in "$chain" -noout -issuer)"
  subject="$(openssl x509 -in "$chain" -noout -subject)"
  [[ "$issuer" != "$subject" ]] || fail "the installed certificate is self-signed: $issuer"
  openssl x509 -in "$chain" -noout -checkend 0 >/dev/null || fail "the installed certificate is expired"

  log "Asserting public readiness over TLS with the system trust store"
  # No -k: the point of this scenario is that an ordinary client trusts the
  # certificate the installer obtained.
  curl -fsS "https://$HOSTNAME_ARG/api/v1/health/ready" >/dev/null ||
    fail "the panel is not publicly ready over HTTPS"
  local redirect
  redirect="$(curl -s -o /dev/null -w '%{http_code}' "http://$HOSTNAME_ARG/")"
  [[ "$redirect" == 30* ]] || fail "plain HTTP does not redirect to HTTPS (HTTP $redirect)"

  assert_services_healthy "fresh TLS install"
  nexa doctor --preflight --allow-existing --json >/dev/null ||
    fail "the post-install preflight does not pass on the machine it just installed"
  assert_admin_can_sign_in "fresh TLS install" "https://$HOSTNAME_ARG"
  seed_canary

  install -d -m 0700 "$STATE_DIR"
  printf '%s\n' "$HOSTNAME_ARG" > "$STATE_DIR/hostname"
  log "fresh-tls passed for $HOSTNAME_ARG"
}

# --- reboot ------------------------------------------------------------------
# Two phases on purpose. A script cannot survive `systemctl reboot`, so the VM
# runner arms the scenario, waits for SSH to come back, then verifies. Anything
# that claimed to do both in one invocation would not be testing a real boot.
scenario_reboot() {
  case "$PHASE" in
    arm)
      assert_services_healthy "before reboot"
      install -d -m 0700 "$STATE_DIR"
      binary_digest > "$STATE_DIR/binary.before-reboot"
      packaging_digest > "$STATE_DIR/packaging.before-reboot"
      log "Rebooting; re-run with --verify once the machine is reachable again"
      systemctl reboot
      ;;
    verify)
      [[ -f "$STATE_DIR/binary.before-reboot" ]] ||
        fail "reboot --verify without a prior --arm; nothing to compare against"
      assert_services_healthy "after reboot"
      [[ "$(binary_digest)" == "$(cat "$STATE_DIR/binary.before-reboot")" ]] ||
        fail "the installed binary changed across the reboot"
      diff -u "$STATE_DIR/packaging.before-reboot" <(packaging_digest) ||
        fail "managed packaging changed across the reboot"
      local base="http://localhost"
      if [[ -f "$STATE_DIR/hostname" ]]; then
        base="https://$(cat "$STATE_DIR/hostname")"
      fi
      curl -fsS "$base/api/v1/health/ready" >/dev/null ||
        fail "the panel is not publicly ready after the reboot"
      assert_admin_can_sign_in "after reboot" "$base"
      assert_canary_intact "after reboot"
      log "reboot passed"
      ;;
    *) fail "reboot needs either --arm or --verify" ;;
  esac
}

# --- N-1 -> N update ---------------------------------------------------------
scenario_update() {
  [[ -n "$PREVIOUS" && -f "$PREVIOUS" ]] || fail "update needs --previous PATH (the N-1 binary)"
  [[ -n "$TARGET"   && -f "$TARGET"   ]] || fail "update needs --target PATH (the N binary)"

  log "Installing N-1 and recording the pre-update host"
  [[ -e /usr/bin/nexa ]] || installer --binary "$PREVIOUS" --allow-insecure-http
  seed_canary
  assert_services_healthy "N-1 install"
  local before_version
  before_version="$(installed_version)"
  local target_version
  target_version="$("$TARGET" version | head -n 1)"
  [[ "$before_version" != "$target_version" ]] ||
    fail "--previous and --target report the same version ($before_version); this proves nothing"

  log "Updating $before_version -> $target_version"
  nexa self-update --binary "$TARGET"

  log "Asserting activation, health, and version"
  [[ "$(installed_version)" == "$target_version" ]] ||
    fail "the installed binary still reports $(installed_version), not $target_version"
  assert_services_healthy "after the update"
  [[ "$(transaction_field phase)" == "succeeded" ]] ||
    fail "the update transaction is in phase $(transaction_field phase), not succeeded"
  # Migrations ran against the SAME database, not a fresh one: the pre-update
  # administrator and the pre-update site data both have to still be there.
  assert_admin_can_sign_in "after the update"
  assert_canary_intact "after the update"
  # Rollback has to remain possible, so the transaction must still hold the
  # binary it replaced.
  local preserved
  preserved="$(transaction_field preservedBinary)"
  [[ -s "$preserved" ]] || fail "the transaction did not preserve the replaced binary"
  [[ "$("$preserved" version | head -n 1)" == "$before_version" ]] ||
    fail "the preserved binary is not $before_version"
  log "update passed ($before_version -> $target_version)"
}

# --- injected update failure -------------------------------------------------
# The injected artifact answers `version` and nothing else. That is the honest
# shape of the failure being tested: the operator validates a candidate by
# running it, so a build that passes validation and then cannot serve is exactly
# the release that must roll the host back completely. The subject under test is
# the operator's rollback, not the fake binary.
scenario_update_failure() {
  [[ -e /usr/bin/nexa ]] || fail "update-failure needs an already-installed node"
  assert_services_healthy "before the injected failure"
  seed_canary

  local before_version before_binary before_packaging
  before_version="$(installed_version)"
  before_binary="$(binary_digest)"
  before_packaging="$(mktemp)"
  packaging_digest > "$before_packaging"

  local broken
  broken="$(mktemp /root/nexa-broken-XXXXXX)"
  cat > "$broken" <<'EOF'
#!/bin/sh
# Injected fault: validates as a runnable nexa binary, then refuses to serve.
[ "$1" = "version" ] && { echo "99.0.0-injected-failure"; exit 0; }
echo "injected update failure" >&2
exit 1
EOF
  chmod 0755 "$broken"

  log "Applying a release that starts but never becomes ready"
  if nexa self-update --binary "$broken"; then
    fail "self-update reported success for a build that cannot serve"
  fi

  log "Asserting the host rolled back completely"
  [[ "$(binary_digest)" == "$before_binary" ]] ||
    fail "the replaced binary was not restored"
  [[ "$(installed_version)" == "$before_version" ]] ||
    fail "the node reports $(installed_version) after rollback, not $before_version"
  diff -u "$before_packaging" <(packaging_digest) ||
    fail "managed packaging did not come back to its pre-update state"
  [[ "$(transaction_field phase)" == "failed" ]] ||
    fail "the failed update left the transaction in phase $(transaction_field phase)"
  assert_services_healthy "after the automatic rollback"
  assert_admin_can_sign_in "after the automatic rollback"
  assert_canary_intact "after the automatic rollback"
  rm -f -- "$broken" "$before_packaging"
  log "update-failure passed; the node is still $before_version"
}

# --- offline rollback --------------------------------------------------------
scenario_offline_rollback() {
  [[ -f "$TRANSACTION" ]] || fail "offline-rollback needs a completed update; run the update scenario first"
  [[ "$(transaction_field phase)" == "succeeded" ]] ||
    fail "offline-rollback needs a succeeded transaction, found $(transaction_field phase)"
  local rollback_target current_version
  current_version="$(installed_version)"
  rollback_target="$(transaction_field previousVersion)"
  seed_canary

  log "Stopping the API and the agent, then rolling back with both down"
  systemctl stop nexa-api.service nexa-agent.service
  # Written as `if`, not `cmd && fail`: under `set -e` the && form exits with
  # the failing left-hand status in the case this scenario actually wants.
  if systemctl is-active --quiet nexa-api.service; then
    fail "nexa-api did not stop; the scenario would not be testing offline recovery"
  fi
  if systemctl is-active --quiet nexa-agent.service; then
    fail "nexa-agent did not stop; the scenario would not be testing offline recovery"
  fi

  # `nexa self-update rollback` is deliberately a local root operation rather
  # than an agent call, precisely so it still works here.
  nexa self-update rollback || fail "the offline rollback failed"

  log "Asserting the node came back on the previous version"
  [[ "$(installed_version)" == "$rollback_target" ]] ||
    fail "the node reports $(installed_version) after rollback, not $rollback_target"
  [[ "$(installed_version)" != "$current_version" ]] ||
    fail "the rollback did not change the installed version"
  assert_services_healthy "after the offline rollback"
  assert_admin_can_sign_in "after the offline rollback"
  assert_canary_intact "after the offline rollback"
  log "offline-rollback passed ($current_version -> $rollback_target)"
}

# --- the whole matrix, for an operator with one throwaway server -------------
# The scenarios chain deliberately, so `all` runs them in the only order that is
# meaningful: fresh-tls installs N-1 with a real certificate, update moves it to
# N, offline-rollback returns it to N-1 using the transaction update wrote, and
# update-failure ends with the transaction in its failed state. The reboot is
# last so the run needs exactly one reconnect — a script cannot outlive the
# reboot it requests, which is why --resume exists.
ALL_PLAN=(
  'fresh TLS install|scenario_fresh_tls'
  'N-1 -> N update|scenario_update'
  'offline rollback with the services stopped|scenario_offline_rollback'
  'injected update failure|scenario_update_failure'
  'reboot|scenario_reboot'
)
RESULT_FILE="$STATE_DIR/all.results"

# One line per scenario: "NUMBER|STATUS|LABEL". Kept on disk rather than in an
# array because the reboot splits the run across two invocations of this script,
# and the summary has to survive that.
record_result() {
  install -d -m 0700 "$STATE_DIR"
  printf '%s|%s|%s\n' "$1" "$2" "$3" >> "$RESULT_FILE"
}

print_summary() {
  local number status label passed=0 failed=0 skipped=0
  printf '\n===== VM lifecycle results =====\n'
  while IFS='|' read -r number status label; do
    [[ -n "$number" ]] || continue
    printf '%d. %-8s %s\n' "$number" "$status" "$label"
    case "$status" in
      PASS)    passed=$((passed + 1)) ;;
      FAIL)    failed=$((failed + 1)) ;;
      *)       skipped=$((skipped + 1)) ;;
    esac
  done < "$RESULT_FILE"
  printf '\n%d passed, %d failed, %d not run\n' "$passed" "$failed" "$skipped"
  [[ "$failed" -eq 0 && "$skipped" -eq 0 ]]
}

scenario_all() {
  local index=0 entry label function status

  if [[ "$PHASE" != "resume" ]]; then
    [[ -n "$HOSTNAME_ARG" ]] || fail "all needs --hostname (a public DNS name pointing at this VM)"
    [[ -n "$TLS_EMAIL" ]]    || fail "all needs --tls-email for the ACME account"
    [[ -n "$PREVIOUS" && -f "$PREVIOUS" ]] || fail "all needs --previous PATH (the N-1 binary)"
    [[ -n "$TARGET"   && -f "$TARGET"   ]] || fail "all needs --target PATH (the N binary)"
    # fresh-tls installs N-1, so the update scenario has something to move off.
    BINARY="$PREVIOUS"
    if [[ "$ASSUME_YES" -ne 1 ]]; then
      printf 'This installs, updates, rolls back, and REBOOTS %s, and writes a test site\n' "$(hostname -f 2>/dev/null || hostname)"
      printf 'under /srv/nexa. Only ever run it on a throwaway server.\n'
      printf 'Type "destroy this host" to continue: '
      local answer
      read -r answer
      [[ "$answer" == "destroy this host" ]] || fail "not confirmed"
    fi
    rm -f -- "$RESULT_FILE"
    install -d -m 0700 "$STATE_DIR"
  else
    [[ -f "$RESULT_FILE" ]] || fail "--resume without a prior run; nothing to resume"
    # The armed reboot is the only thing --resume can be resuming.
    PHASE=verify
    log "Scenario 5/5: reboot (verifying the machine that just came back)"
    if scenario_reboot; then
      record_result 5 PASS 'reboot'
    else
      record_result 5 FAIL 'reboot'
    fi
    print_summary
    return
  fi

  for entry in "${ALL_PLAN[@]}"; do
    index=$((index + 1))
    label="${entry%%|*}"
    function="${entry##*|}"
    log "Scenario $index/${#ALL_PLAN[@]}: $label"
    if [[ "$function" == "scenario_reboot" ]]; then
      # Arming the reboot ends this invocation. Nothing is recorded for it here:
      # --resume writes the real verdict, so the summary the operator finally
      # reads has one line per scenario and no placeholder to misread as a pass.
      PHASE=arm
      printf '\nScenarios 1-%d passed. Rebooting now.\n' "$((index - 1))"
      printf 'Reconnect and run: %s all --resume\n' "$0"
      scenario_reboot
      return
    fi
    # A subshell, so a scenario's `fail` ends that scenario rather than the run:
    # the summary must be able to name which one stopped it.
    if ( "$function" ); then
      status=PASS
    else
      status=FAIL
    fi
    record_result "$index" "$status" "$label"
    if [[ "$status" == FAIL ]]; then
      while [[ $index -lt ${#ALL_PLAN[@]} ]]; do
        index=$((index + 1))
        record_result "$index" 'NOT RUN' "${ALL_PLAN[index - 1]%%|*}"
      done
      print_summary
      return 1
    fi
  done
}

case "$SCENARIO" in
  all)              scenario_all ;;
  fresh-tls)        scenario_fresh_tls ;;
  reboot)           scenario_reboot ;;
  update)           scenario_update ;;
  update-failure)   scenario_update_failure ;;
  offline-rollback) scenario_offline_rollback ;;
  *) fail "unknown scenario '$SCENARIO' (all, fresh-tls, reboot, update, update-failure, offline-rollback)" ;;
esac
