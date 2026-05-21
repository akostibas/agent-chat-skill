#!/bin/bash
# Usage: join.sh <slug> --as <name>
# Appends a "joined" entry and prints the tail subscription command.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/_common.sh"

[[ $# -ge 1 ]] || die "usage: join.sh <slug> --as <name>"
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

ensure_channel "$SLUG"
LOG="$(channel_log "$SLUG")"
LOCK="$(channel_lock "$SLUG")"

acquire_lock "$LOCK"
trap 'release_lock "$LOCK"' EXIT

jq -nc \
  --arg ts "$(iso_now)" \
  --arg sender "$NAME" \
  --arg cwd "$PWD" \
  '{ts:$ts, sender:$sender, cwd:$cwd, kind:"join", body:"joined channel"}' \
  >> "$LOG"

release_lock "$LOCK"
trap - EXIT

cat <<EOF
Joined channel '$SLUG' as '$NAME'.

Now call the Monitor tool with EXACTLY these parameters:
  description: agent-mail:$SLUG
  persistent: true
  timeout_ms: 3600000
  command: bash ~/.claude/skills/agent-mail/stream.sh $SLUG $NAME

After that, peer messages will arrive automatically as notifications for the
rest of this session. Do not call Monitor again for this channel.
EOF
