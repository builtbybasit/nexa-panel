#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${1:-amd64}"
VERSION="${VERSION:-0.1.0-dev}"
COMMIT="${COMMIT:-unknown}"
BUILT_AT="${BUILT_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
OUTPUT="${ROOT_DIR}/dist/nexa-linux-${ARCH}"
export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/nexa-panel-go-cache}"

case "${ARCH}" in
  amd64|arm64) ;;
  *)
    echo "unsupported architecture: ${ARCH}" >&2
    exit 2
    ;;
esac

cd "${ROOT_DIR}/web"
bun install --frozen-lockfile
bun run typecheck
bun run test
bun run build

cd "${ROOT_DIR}"
go test ./...
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH="${ARCH}" go build \
  -tags embed \
  -trimpath \
  -ldflags "-s -w -X github.com/nexa-panel/nexa-panel/internal/platform/version.Version=${VERSION} -X github.com/nexa-panel/nexa-panel/internal/platform/version.Commit=${COMMIT} -X github.com/nexa-panel/nexa-panel/internal/platform/version.BuiltAt=${BUILT_AT}" \
  -o "${OUTPUT}" \
  ./cmd/nexa

if command -v sha256sum >/dev/null 2>&1; then
  (
    cd "$(dirname "${OUTPUT}")"
    sha256sum "$(basename "${OUTPUT}")"
  ) > "${OUTPUT}.sha256"
else
  (
    cd "$(dirname "${OUTPUT}")"
    shasum -a 256 "$(basename "${OUTPUT}")"
  ) > "${OUTPUT}.sha256"
fi

echo "built ${OUTPUT}"
