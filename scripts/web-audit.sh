#!/usr/bin/env bash
# Runs `bun audit` as a release gate: it fails the build on a reported advisory,
# but tolerates the advisory registry being unreachable. Bun's audit endpoint
# answers 5xx/429 during outages, and a dependency-registry outage must not be
# able to block a tagged release when nothing about the code has changed — a
# genuine advisory still must. So a transport/service failure is downgraded to a
# loud warning, while any other non-zero exit is surfaced as a failure.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUN="${BUN:-bun}"

cd "${ROOT_DIR}/web" || exit 1

output=""
status=0
output="$("${BUN}" audit 2>&1)" || status=$?

printf '%s\n' "${output}"

if [[ ${status} -eq 0 ]]; then
  exit 0
fi

# A transport/service failure is Bun failing to reach or get a usable answer
# from the registry, never an advisory. Match those specific shapes rather than
# any non-zero exit, so a real vulnerability is never silently tolerated.
if grep -qiE 'audit request failed|status (4[0-9][0-9]|5[0-9][0-9])|fetch failed|ECONNRESET|ECONNREFUSED|ETIMEDOUT|ENOTFOUND|EAI_AGAIN|unable to connect|timed out' <<<"${output}"; then
  {
    echo ""
    echo "warning: bun audit could not reach the advisory registry (transport/service error);"
    echo "         skipping the audit gate for this run. Re-run 'make web-audit' once the"
    echo "         service is reachable to complete the check."
  } >&2
  exit 0
fi

# Any other non-zero exit is a real finding (or a genuine misuse) and must fail.
exit "${status}"
