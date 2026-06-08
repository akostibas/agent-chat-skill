#!/bin/bash
# teardown-probe.sh — diagnostic harness to learn HOW Claude Code's Monitor
# tears down a persistent child when the session closes.
#
# Run it via the Monitor tool (persistent:true), then CLOSE the Claude session.
# From a fresh session, read $PROBE_LOG and look for:
#   * a "SIGNAL <name> received" line  -> teardown delivers a trappable signal
#                                         (a trap-based leave CAN work)
#   * heartbeats continuing with ppid=1 -> child was orphaned, not signalled
#                                         (process leaks; trap never fires)
#   * neither (log just stops)          -> hard SIGKILL (un-trappable)
#
# Usage (direct):  PROBE_LOG=/tmp/probe.log bin/teardown-probe.sh
# Usage (Monitor): command = bash <path>/bin/teardown-probe.sh
set -uo pipefail

PROBE_LOG="${PROBE_LOG:-/tmp/agent-chat-teardown-probe.log}"
log() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $*" >>"$PROBE_LOG"; }

: >"$PROBE_LOG"
PGID="$(ps -o pgid= -p $$ | tr -d ' ')"
log "START pid=$$ ppid=$PPID pgid=$PGID"

# Trap every catchable signal teardown might plausibly use.
for sig in INT TERM HUP QUIT PIPE USR1 USR2; do
  # shellcheck disable=SC2064
  trap "log \"SIGNAL $sig received (ppid=\$PPID)\"; log END_VIA_$sig; exit 0" "$sig"
done
trap 'log "EXIT trap fired"' EXIT

# Mirror stream.sh's shape: a backgrounded loop + wait, so a signal interrupts
# the wait and the trap runs. Heartbeat logs PPID each tick so a reparent-to-1
# (orphan) is visible even if no signal ever arrives.
( while true; do log "heartbeat ppid=$PPID"; sleep 1; done ) &

# Emit to stdout too (every 5s) so Monitor has an event stream and so closing
# the read end would deliver SIGPIPE if that's the teardown mechanism.
while true; do
  echo "probe alive $(date -u +%Y-%m-%dT%H:%M:%SZ) ppid=$PPID"
  sleep 5
done
