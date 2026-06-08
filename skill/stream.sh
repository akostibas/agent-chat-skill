#!/bin/bash
# Usage: stream.sh <slug> <name>
# Invoked by Monitor. Tails the channel log and emits peer messages,
# one line per text line with the verified sender prefixed.
# First line of each message includes the timestamp + kind.
#
# Departure is announced two ways (see ADR-0003):
#   1. Graceful path — a trap on INT/TERM/HUP emits a "leave" immediately.
#   2. Hard path — Claude Code's Monitor SIGKILLs this child on session close,
#      so no trap can fire. Instead we heartbeat a presence file; a surviving
#      peer's reaper notices it go stale and emits the leave on our behalf.
# Both paths are idempotent: exactly one leave is posted per departure, because
# a graceful exit removes the presence file so no peer re-reaps it.

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
PRESENCE="$(presence_file "$SLUG" "$NAME")"
[[ -f "$LOG" ]] || die "no such channel: $SLUG (run join.sh first)"

# Announce our own departure once, best-effort. Never block or fail shutdown:
# short lock timeout, all errors swallowed. Broadcast (no mentions) so every
# peer is notified. Removing the presence file is what guarantees a peer's
# reaper won't post a second, redundant leave for us.
LEFT=0
emit_leave() {
  [[ "$LEFT" -eq 1 ]] && return 0
  LEFT=1
  emit_leave_event "$SLUG" "$NAME"
}

# Idempotent teardown: announce, drop our presence, stop the children.
HEARTBEAT_PID=""
cleanup() {
  emit_leave
  rm -f "$PRESENCE" 2>/dev/null || true
  if [[ -n "$HEARTBEAT_PID" ]]; then kill "$HEARTBEAT_PID" 2>/dev/null || true; fi
  if [[ -n "${STREAM_PID:-}" ]]; then kill "$STREAM_PID" 2>/dev/null || true; fi
}
trap cleanup EXIT
trap 'cleanup; exit 0' INT TERM HUP

# Heartbeat: refresh our presence and reap any vanished peers on each tick.
# This is the engine behind the hard-kill path — it's how *this* agent posts
# leaves for peers that were SIGKILLed, and how peers will post ours.
touch_presence "$SLUG" "$NAME"
(
  while true; do
    touch_presence "$SLUG" "$NAME"
    reap_stale_peers "$SLUG" "$NAME"
    sleep "$AGENT_CHAT_HEARTBEAT_SECS"
  done
) &
HEARTBEAT_PID=$!

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
