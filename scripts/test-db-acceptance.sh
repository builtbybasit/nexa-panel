#!/usr/bin/env bash
# Destructive database acceptance suites, executed rather than skipped.
#
# Both suites destroy a real database and restore it through the operator, so
# they are gated behind an environment variable and never run as part of
# `go test ./...`. This script is what turns them on — in CI and on a laptop —
# so the gate has exactly one documented way to be opened.
#
#   bash scripts/test-db-acceptance.sh            # both engines
#   bash scripts/test-db-acceptance.sh mysql      # MySQL 8.4 + MariaDB 11.8
#   bash scripts/test-db-acceptance.sh postgres   # PostgreSQL 18
#
# MySQL runs on the host: its suite starts its own mysql/mariadb containers and
# drives them with `docker exec`, so it only needs a Docker daemon.
#
# PostgreSQL is the opposite. Its suite shells out to
# /usr/lib/postgresql/18/bin as root through `runuser -u postgres`, so it has to
# run INSIDE a PostgreSQL 18 host, not next to one. Rather than install PGDG on
# the runner (where an existing cluster would take port 5432 and push the new
# one to 5433, which the suite's pg_lsclusters stub does not describe), the test
# binary is cross-compiled for the container's platform and executed in the
# official postgres:18 image.
set -euo pipefail

SUITE="${1:-all}"
GO="${GO:-go}"
POSTGRES_IMAGE="${NEXA_POSTGRES_IMAGE:-postgres:18}"
POSTGRES_CONTAINER="${NEXA_POSTGRES_CONTAINER:-nexa-postgres-acceptance}"
READY_ATTEMPTS="${NEXA_DB_READY_ATTEMPTS:-90}"

case "$SUITE" in
  all|mysql|postgres) ;;
  *) echo "usage: test-db-acceptance.sh [all|mysql|postgres]" >&2; exit 2 ;;
esac

command -v docker >/dev/null 2>&1 || { echo "error: docker is required" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "error: the Docker daemon is not reachable" >&2; exit 1; }

run_mysql() {
  echo "==> MySQL/MariaDB destroyed-database restore acceptance"
  NEXA_MYSQL_INTEGRATION=1 "$GO" test ./internal/platform/operators/mysql/ \
    -run TestMySQLFamilyDestroyedDatabaseRestoreIntegration -v -count=1 -timeout 25m
}

run_postgres() {
  echo "==> PostgreSQL 18 destroyed-database restore acceptance"
  local workdir platform arch
  workdir="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf -- '$workdir'; docker rm -f '$POSTGRES_CONTAINER' >/dev/null 2>&1 || true" RETURN

  # The test binary has to match the image's architecture, not the host's: a
  # Docker daemon will happily run an amd64 image on arm64 under emulation.
  platform="$(docker image inspect "$POSTGRES_IMAGE" --format '{{.Os}}/{{.Architecture}}' 2>/dev/null || true)"
  if [[ -z "$platform" ]]; then
    docker pull -q "$POSTGRES_IMAGE" >/dev/null
    platform="$(docker image inspect "$POSTGRES_IMAGE" --format '{{.Os}}/{{.Architecture}}')"
  fi
  arch="${platform##*/}"
  CGO_ENABLED=0 GOOS="${platform%%/*}" GOARCH="$arch" \
    "$GO" test -c -o "$workdir/postgres.test" ./internal/platform/operators/postgres/

  docker rm -f "$POSTGRES_CONTAINER" >/dev/null 2>&1 || true
  docker run -d --name "$POSTGRES_CONTAINER" \
    -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_PASSWORD=acceptance \
    "$POSTGRES_IMAGE" >/dev/null

  # The image's entrypoint initialises the cluster with a temporary server on
  # the very socket the suite connects to, then shuts it down and starts the
  # real one. pg_isready answers during that bootstrap window — roughly 130ms —
  # so waiting on readiness alone can hand back a server that vanishes before
  # the test connects, which is the intermittent
  #   psql: connection to server on socket ".s.PGSQL.5432" failed:
  #   No such file or directory
  # seen in CI. Wait for the entrypoint to announce that initialisation is done
  # first; only the real server can be ready after that. The container is
  # always freshly created above, so this marker is always printed.
  local initialised=0
  for _ in $(seq 1 "$READY_ATTEMPTS"); do
    if docker logs "$POSTGRES_CONTAINER" 2>&1 | grep -q 'init process complete; ready for start up'; then
      initialised=1
      break
    fi
    sleep 1
  done
  if [[ "$initialised" -ne 1 ]]; then
    docker logs --tail 100 "$POSTGRES_CONTAINER" >&2 || true
    echo "error: $POSTGRES_IMAGE did not finish initialising" >&2
    return 1
  fi

  local ready=0
  for _ in $(seq 1 "$READY_ATTEMPTS"); do
    if docker exec "$POSTGRES_CONTAINER" pg_isready -h /var/run/postgresql >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 1
  done
  if [[ "$ready" -ne 1 ]]; then
    docker logs --tail 100 "$POSTGRES_CONTAINER" >&2 || true
    echo "error: $POSTGRES_IMAGE did not become ready" >&2
    return 1
  fi

  docker cp "$workdir/postgres.test" "$POSTGRES_CONTAINER:/tmp/postgres.test"
  docker exec -e NEXA_POSTGRES_INTEGRATION=1 "$POSTGRES_CONTAINER" \
    /tmp/postgres.test -test.run TestPostgres18DestroyedDatabaseRestoreIntegration -test.v -test.timeout 10m
}

if [[ "$SUITE" == "all" || "$SUITE" == "mysql" ]]; then
  run_mysql
fi
if [[ "$SUITE" == "all" || "$SUITE" == "postgres" ]]; then
  run_postgres
fi

echo "Database acceptance suites passed ($SUITE)"
