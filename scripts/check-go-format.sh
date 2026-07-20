#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

format_flag="-l"
if [[ "${1:-}" == "--write" ]]; then
  format_flag="-w"
  shift
fi
if [[ $# -ne 0 ]]; then
  echo "usage: check-go-format.sh [--write]" >&2
  exit 2
fi

files=()
while IFS= read -r -d '' file; do
  files+=("$file")
done < <(find cmd internal packaging -type f -name '*.go' -print0)

if [[ "$format_flag" == "-w" ]]; then
  "${GO:-go}" run mvdan.cc/gofumpt@v0.10.0 -w "${files[@]}"
  exit 0
fi

unformatted="$("${GO:-go}" run mvdan.cc/gofumpt@v0.10.0 -l "${files[@]}")"
if [[ -n "$unformatted" ]]; then
  echo "Go files must be formatted with gofumpt v0.10.0:" >&2
  echo "$unformatted" >&2
  exit 1
fi
