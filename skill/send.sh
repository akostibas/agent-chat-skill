#!/bin/bash
# Usage: send.sh <slug> --as <name>    (message body on stdin)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/_common.sh"

[[ $# -ge 1 ]] || die "usage: send.sh <slug> --as <name>  (body on stdin)"
SLUG="$1"; shift
NAME=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --as) NAME="${2:-}"; shift 2 ;;
    *) die "unknown arg: $1" ;;
  esac
done

[[ -n "$NAME" ]] || die "missing --as <name>"
validate_ident slug "$SLUG"
validate_ident name "$NAME"

BODY="$(cat)"
[[ -n "$BODY" ]] || die "empty message body (provide on stdin)"

ensure_channel "$SLUG"
LOG="$(channel_log "$SLUG")"
LOCK="$(channel_lock "$SLUG")"

acquire_lock "$LOCK"
trap 'release_lock "$LOCK"' EXIT

jq -nc \
  --arg ts "$(iso_now)" \
  --arg sender "$NAME" \
  --arg cwd "$(agent_cwd)" \
  --arg branch "$(agent_branch)" \
  --arg body "$BODY" \
  '{ts:$ts, sender:$sender, cwd:$cwd, branch:$branch, kind:"msg", body:$body}' \
  >> "$LOG"

release_lock "$LOCK"
trap - EXIT

echo "sent ($(wc -c <<<"$BODY" | tr -d ' ') bytes) to '$SLUG' as '$NAME'"
