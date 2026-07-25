#!/usr/bin/env bash
# Regenerate the embedded OpenAPI artifacts consumed by the control panel:
#   internal/platform/httpapi/apispec/openapi.gen.json  (bundled contract, embedded for spec-driven routing)
#   internal/platform/httpapi/apispec/models.gen.go     (oapi-codegen request/response models)
#
# The multi-file spec under openapi/ is first bundled into one document with
# redocly (already vendored in web/node_modules), then oapi-codegen emits the
# Go models. Run after editing anything under openapi/.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

apispec_dir="internal/platform/httpapi/apispec"
bundle="$apispec_dir/openapi.gen.json"
models="$apispec_dir/models.gen.go"
redocly="web/node_modules/.bin/redocly"

if [[ ! -x "$redocly" ]]; then
  echo "redocly not found at $redocly; run 'make web-install' first" >&2
  exit 1
fi

echo "bundling openapi/openapi.yaml -> $bundle"
"$redocly" bundle openapi/openapi.yaml -o "$bundle" >/dev/null

echo "generating models -> $models"
# Pin the generator through the module tool directive so the version is reproducible.
go tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen \
  -package apispec \
  -generate models \
  -o "$models" \
  "$bundle"

echo "openapi artifacts regenerated"
