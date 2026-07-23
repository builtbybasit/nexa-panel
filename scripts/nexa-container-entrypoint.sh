#!/bin/sh
# Container entrypoint for the disposable Ubuntu test node.
#
# Once systemd takes over as PID 1 it repoints its own stdin/stdout/stderr at
# /dev/null, and this image has no /dev/console device — so nothing a unit
# prints ever reaches `docker compose logs`, however its StandardOutput is set.
#
# Fork a relay FIRST: the child inherits THIS process's stdout, which is the
# pipe docker captures, and keeps it across the exec below. It relays the
# credentials banner that nexa-firstadmin.service drops under /run, so
# `docker compose logs` shows working credentials for the node.
set -eu

BANNER="/run/nexa-panel/first-admin.banner"

(
  waited=0
  while [ "$waited" -lt 300 ]; do
    if [ -f "$BANNER" ]; then
      cat "$BANNER"
      break
    fi
    waited=$((waited + 1))
    sleep 1
  done
) &

exec /lib/systemd/systemd
