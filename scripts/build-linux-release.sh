#!/usr/bin/env bash
# Build the Linux release artifacts for one architecture.
#
# Produces three files under dist/:
#
#   nexa-linux-<arch>                     the bare binary
#   nexa-panel-linux-<arch>.tar.gz        the installable bundle
#   nexa-panel-linux-<arch>.tar.gz.sha256
#
# The bundle is what an operator (and the self-update operator) actually
# installs from: scripts/install.sh reads the packaging tree and the seed helper
# out of the checkout it lives in, so shipping the binary alone leaves cloning
# the private repo as the only working install path. The tarball carries every
# repo file install.sh touches, laid out exactly as install.sh expects to find
# them relative to itself.
#
# The bare binary is a local build product only — the Docker test node and
# `self-update --binary` consume it off this machine. It is deliberately NOT a
# release asset and gets no checksum sidecar: a released bare binary would sit
# beside the signed bundles carrying no signature at all, which is an invitation
# to install the one artifact whose authenticity nothing verifies.
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

# Emit a `sha256sum -c`-compatible sidecar next to the artifact. The digest line
# names the bare filename, never a path, so `sha256sum -c` verifies from inside
# dist/ (and from inside whatever directory the asset was downloaded into).
write_checksum() {
  local artifact="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    (
      cd "$(dirname "${artifact}")"
      sha256sum "$(basename "${artifact}")"
    ) > "${artifact}.sha256"
  else
    (
      cd "$(dirname "${artifact}")"
      shasum -a 256 "$(basename "${artifact}")"
    ) > "${artifact}.sha256"
  fi
}

# --- installable bundle -------------------------------------------------------
# The file list is not a guess: it is every path scripts/install.sh resolves out
# of its own checkout ("$ROOT_DIR/..."). The whole packaging tree ships verbatim
# so a packaging file added later is carried without touching this script.
BUNDLE_NAME="nexa-panel-${VERSION}-linux-${ARCH}"
TARBALL="${ROOT_DIR}/dist/nexa-panel-linux-${ARCH}.tar.gz"
STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nexa-release.XXXXXX")"
trap 'rm -rf "${STAGE_DIR}"' EXIT
BUNDLE_DIR="${STAGE_DIR}/${BUNDLE_NAME}"

install -d -m 0755 "${BUNDLE_DIR}/bin" "${BUNDLE_DIR}/scripts"
install -m 0755 "${OUTPUT}" "${BUNDLE_DIR}/bin/nexa"
install -m 0755 "${ROOT_DIR}/scripts/install.sh" "${BUNDLE_DIR}/scripts/install.sh"
install -m 0755 "${ROOT_DIR}/scripts/nexa-seed-admin.sh" "${BUNDLE_DIR}/scripts/nexa-seed-admin.sh"
install -m 0755 "${ROOT_DIR}/scripts/nexa-release-helper.py" "${BUNDLE_DIR}/scripts/nexa-release-helper.py"
install -m 0755 "${ROOT_DIR}/scripts/uninstall.sh" "${BUNDLE_DIR}/scripts/uninstall.sh"

cp -R "${ROOT_DIR}/packaging" "${BUNDLE_DIR}/packaging"
# Drop editor and macOS cruft the checkout may carry, then normalise modes so the
# bundle does not inherit a developer's umask.
find "${BUNDLE_DIR}/packaging" -name '.DS_Store' -delete
find "${BUNDLE_DIR}/packaging" -type d -exec chmod 0755 {} +
find "${BUNDLE_DIR}/packaging" -type f -exec chmod 0644 {} +

cat > "${BUNDLE_DIR}/RELEASE" <<EOF
version=${VERSION}
commit=${COMMIT}
arch=${ARCH}
built_at=${BUILT_AT}
EOF
chmod 0644 "${BUNDLE_DIR}/RELEASE"

# Reproducible-ish packaging: identical inputs should give an identical tarball.
# GNU tar can do all of it (stable ordering, zeroed ownership, fixed mtimes).
# bsdtar (the macOS default) has no --sort and no --mtime, so there the mtimes
# are stamped onto the staged files with `touch` beforehand and the ordering is
# whatever the filesystem yields — local macOS builds are therefore not
# bit-reproducible, while the release workflow (GNU tar on Ubuntu) is. `touch -t`
# also reads its stamp as local time, so those mtimes sit BUILT_AT's digits in
# the builder's timezone rather than at BUILT_AT itself; nothing reads them.
# gzip -n in both cases, so the compressed stream carries no timestamp either.
TAR_STAMP="${BUILT_AT//[-:TZ]/}"                    # 2026-07-22T09:08:07Z -> 20260722090807
TOUCH_STAMP="${TAR_STAMP:0:12}.${TAR_STAMP:12:2}"   # -> 202607220908.07
rm -f "${TARBALL}" "${TARBALL%.gz}"
if tar --version 2>/dev/null | head -n 1 | grep -q 'GNU tar'; then
  tar --sort=name \
    --owner=0 --group=0 --numeric-owner \
    --mtime="${BUILT_AT}" \
    -cf "${TARBALL%.gz}" -C "${STAGE_DIR}" "${BUNDLE_NAME}"
else
  find "${BUNDLE_DIR}" -exec touch -t "${TOUCH_STAMP}" {} +
  COPYFILE_DISABLE=1 tar --uid 0 --gid 0 --uname '' --gname '' \
    -cf "${TARBALL%.gz}" -C "${STAGE_DIR}" "${BUNDLE_NAME}"
fi
gzip -n -9 "${TARBALL%.gz}"

write_checksum "${TARBALL}"

echo "built ${OUTPUT}"
echo "built ${TARBALL}"
