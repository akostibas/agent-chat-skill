#!/usr/bin/env bash
# End-to-end round-trip test for the containerized worker (issue #8 AC #2/#3).
#
# Spins up a worker container against a THROWAWAY channel dir, waits for it to
# join, sends it a task from a host-side peer, and asserts it replies on the
# channel — full round-trip, no external dispatcher involved. Tears down after.
#
# Run it yourself (`! bin/docker-worker-test.sh`): it extracts subscription
# creds from the Keychain via bin/docker-worker.sh, so the token stays out of
# band. Override the image with IMAGE=... and auth with --api-key passthrough.
set -euo pipefail

cd "$(dirname "$0")/.."
IMAGE="${IMAGE:-agent-chat-worker}"
SLUG="smoke-$$"
WORKER="smoke-worker"
PEER="host-tester"
SCRATCH="$(mktemp -d -t agent-chat-smoke.XXXXXX)"
CONTAINER="agent-chat-worker-$SLUG"
LOG="$SCRATCH/$SLUG/log"
HOST_BIN="cmd/agent-chat/agent-chat"

pass=0
say()  { printf '\n=== %s ===\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; pass=1; }

# shellcheck disable=SC2317  # reached indirectly via 'trap cleanup EXIT'
cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

# Host-side peer needs the binary to send/read on the scratch channel.
say "build host binary"
go build -o "$HOST_BIN" ./cmd/agent-chat/

# The peer JOINS before sending. `send --as` works without joining, but an
# unjoined sender has no presence file, so it is not a resolvable @name — the
# worker's directed reply gets refused and it has to fall back to a broadcast.
say "host peer joins the channel"
AGENT_CHAT_ROOT="$SCRATCH" "$HOST_BIN" join "$SLUG" --as "$PEER" >/dev/null

# wait_for <seconds> <jq-filter-matching-a-log-record>
wait_for() {
  local timeout="$1" filter="$2" t=0
  while (( t < timeout )); do
    if [[ -f "$LOG" ]] && jq -e "$filter" "$LOG" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2; t=$((t+2))
  done
  return 1
}

say "launch worker container on throwaway channel '$SLUG'"
bin/docker-worker.sh "$SLUG" --name "$WORKER" --root "$SCRATCH" --image "$IMAGE" \
  || { fail "launcher failed"; exit 1; }

# Readiness = the worker's announcement broadcast, NOT the join record. join.sh
# writes the join record BEFORE the session arms its doorbell; the seed
# prompt posts the announcement AFTER it. So an announcement proves the worker
# is actually subscribed — sending on the join record alone races the
# join->subscribe gap and the task is never delivered.
say "wait for worker to be ready (join + doorbell arm + announce, up to 150s)"
if wait_for 150 'select(.sender=="'"$WORKER"'" and .kind=="msg")'; then
  echo "OK: worker '$WORKER' announced — joined, subscribed, and ready"
else
  fail "worker never announced — inspect: docker logs $CONTAINER; docker exec -it $CONTAINER tmux attach -t worker"
  exit 1
fi

say "send a task to the worker from host peer '$PEER'"
AGENT_CHAT_ROOT="$SCRATCH" "$HOST_BIN" send "$SLUG" --as "$PEER" <<EOF
@$WORKER respond on this channel, addressed to me, with exactly the token
ROUNDTRIP-OK so I can confirm the round-trip works. Send exactly: @$PEER ROUNDTRIP-OK
EOF
SENT_TS="$(date -u +%Y-%m-%dT%H:%M:%S)"
echo "sent at $SENT_TS"

say "wait for worker reply (hook delivery + a turn, up to 120s)"
if wait_for 120 'select(.sender=="'"$WORKER"'" and .kind=="msg" and (.body|test("ROUNDTRIP-OK")))'; then
  echo "OK: worker replied with ROUNDTRIP-OK"
  echo "--- worker's message ---"
  jq -r 'select(.sender=="'"$WORKER"'" and .kind=="msg") | .body' "$LOG" | tail -3
else
  fail "no ROUNDTRIP-OK reply from worker"
  echo "--- full channel log for diagnosis ---"
  jq -r '"\(.ts) [\(.kind)] \(.sender): \(.body)"' "$LOG" 2>/dev/null || cat "$LOG"
fi

if [[ "$pass" == 0 ]]; then
  printf '\n\xe2\x9c\x94 ROUND-TRIP PASSED — containerized worker joined and replied.\n'
else
  printf '\n\xe2\x9c\x98 ROUND-TRIP FAILED — see diagnostics above.\n'
fi
exit "$pass"
