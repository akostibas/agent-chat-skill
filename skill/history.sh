#!/bin/bash
# Usage: history.sh <slug> [--since <iso8601>]

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/_common.sh"

[[ $# -ge 1 ]] || die "usage: history.sh <slug> [--since <iso8601>]"
SLUG="$1"; shift
SINCE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --since) SINCE="${2:-}"; shift 2 ;;
    *) die "unknown arg: $1" ;;
  esac
done

validate_ident slug "$SLUG"
LOG="$(channel_log "$SLUG")"
[[ -f "$LOG" ]] || die "no such channel: $SLUG"

if [[ -n "$SINCE" ]]; then
  jq -rc --arg since "$SINCE" \
    'select(.ts >= $since) | "━━━ \(.ts) \(.sender) (cwd=\(.cwd)\(if (.branch // "") != "" then " branch=\(.branch)" else "" end)) [\(.kind)] ━━━\n\(.body)\n"' \
    "$LOG"
else
  jq -rc \
    '"━━━ \(.ts) \(.sender) (cwd=\(.cwd)\(if (.branch // "") != "" then " branch=\(.branch)" else "" end)) [\(.kind)] ━━━\n\(.body)\n"' \
    "$LOG"
fi
