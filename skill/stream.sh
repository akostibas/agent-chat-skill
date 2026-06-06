#!/bin/bash
# Usage: stream.sh <slug> <name>
# Invoked by Monitor. Tails the channel log and emits peer messages,
# one line per text line with the verified sender prefixed.
# First line of each message includes the timestamp + kind.
#
# On teardown (session close, TaskStop) it emits a best-effort "leave" event
# so peers learn this agent is gone — the mirror of join.sh's "join". This only
# fires for trappable signals; a hard SIGKILL leaves no trace.

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
LOCK="$(channel_lock "$SLUG")"
[[ -f "$LOG" ]] || die "no such channel: $SLUG (run join.sh first)"

# Announce departure once, best-effort. Never block or fail shutdown: short
# lock timeout, all errors swallowed. Broadcast (no mentions) so every peer
# is notified.
LEFT=0
emit_leave() {
  [[ "$LEFT" -eq 1 ]] && return 0
  LEFT=1
  (
    acquire_lock "$LOCK" 2
    jq -nc \
      --arg ts "$(iso_now)" \
      --arg sender "$NAME" \
      '{ts:$ts, sender:$sender, kind:"leave", body:"left channel"}' \
      >> "$LOG"
    release_lock "$LOCK"
  ) 2>/dev/null || true
}
trap 'emit_leave; exit 0' INT TERM HUP

# Stream in the background and `wait`, so a teardown signal interrupts the wait
# and the trap runs. `exec`-ing the pipeline would replace this shell and the
# trap would never fire.
#
# Per-line sender prefix prevents header-spoofing via embedded newlines:
# every line carries the verified .sender token, never raw body text.
tail -n 0 -F "$LOG" | jq --unbuffered -r --arg me "$NAME" '
  . as $m |
  select($m.sender != $me) |
  # Mention filter: msg with non-empty mentions only emits if I am mentioned.
  # Empty/missing mentions = broadcast. Non-msg kinds (join, leave) bypass.
  select(
    $m.kind != "msg"
    or (($m.mentions // []) | length == 0)
    or (($m.mentions // []) | any(. == $me))
  ) |
  ($m.sender + " │ [" + $m.ts + " " + $m.kind + "]"
    + (if ($m.cwd // "") != "" then " cwd=" + $m.cwd else "" end)
    + (if ($m.branch // "") != "" then " branch=" + $m.branch else "" end)),
  ($m.body | split("\n")[] | $m.sender + " │ " + .)
' &
STREAM_PID=$!
wait "$STREAM_PID"
