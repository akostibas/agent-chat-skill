#!/bin/bash
# Usage: stream.sh <slug> <name>
# Invoked by Monitor. Tails the channel log and emits peer messages,
# one line per text line with the verified sender prefixed.
# First line of each message includes the timestamp + kind.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/_common.sh"

[[ $# -eq 2 ]] || die "usage: stream.sh <slug> <name>"
SLUG="$1"
NAME="$2"
validate_ident slug "$SLUG"
validate_ident name "$NAME"

LOG="$(channel_log "$SLUG")"
[[ -f "$LOG" ]] || die "no such channel: $SLUG (run join.sh first)"

# Per-line sender prefix prevents header-spoofing via embedded newlines:
# every line carries the verified .sender token, never raw body text.
exec tail -n 0 -F "$LOG" | jq --unbuffered -r --arg me "$NAME" '
  . as $m |
  select($m.sender != $me) |
  ($m.sender + " │ [" + $m.ts + " " + $m.kind + "]"
    + (if ($m.cwd // "") != "" then " cwd=" + $m.cwd else "" end)
    + (if ($m.branch // "") != "" then " branch=" + $m.branch else "" end)),
  ($m.body | split("\n")[] | $m.sender + " │ " + .)
'
