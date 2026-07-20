#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${1:-arm64}"
if [[ -z "${VERSION:-}" ]]; then
  VERSION="$(git -C "$ROOT_DIR" describe --tags --exact-match 2>/dev/null || true)"
  VERSION="${VERSION:-0.1.0-dev}"
fi
if [[ -z "${COMMIT:-}" ]]; then
  COMMIT="$(git -C "$ROOT_DIR" rev-parse --short=12 HEAD 2>/dev/null || true)"
  if [[ -n "$COMMIT" ]] && [[ -n "$(git -C "$ROOT_DIR" status --porcelain 2>/dev/null)" ]]; then
    COMMIT="${COMMIT}-dirty"
  fi
  COMMIT="${COMMIT:-unknown}"
fi
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

make -C "${ROOT_DIR}" check

cd "${ROOT_DIR}"
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
