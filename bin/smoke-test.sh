#!/usr/bin/env bash
#
# smoke-test.sh — end-to-end check of the agent-chat skill scripts.
#
# Exercises join.sh, send.sh, and history.sh against a throwaway channel under
# a temp HOME so it can't collide with real chat state. Doesn't touch Monitor —
# that's a Claude Code primitive, not something we can drive from a shell.
#
# Usage:
#   bin/smoke-test.sh
#
# Exit codes: 0 = pass, 1 = setup/precondition failure, 2 = behavior mismatch.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# --- Preconditions ---

for cmd in jq shlock; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "FAIL: required command '$cmd' not on PATH." >&2
    exit 1
  fi
done

# --- Install into an isolated HOME ---

tmphome="$(mktemp -d)"
trap 'rm -rf "$tmphome"' EXIT
export HOME="$tmphome"

skill_dir="$HOME/.claude/skills/agent-chat"
mkdir -p "$skill_dir"
cp -R skill/. "$skill_dir/"
chmod +x "$skill_dir"/*.sh
echo "Installed skill -> $skill_dir"

slug="smoke-$$"
name="smoke-tester"
peer="smoke-peer"
body="ping from smoke test $$"

# --- Join (creates the channel) ---

echo "Joining channel $slug as $name..."
join_out="$(bash "$skill_dir/join.sh" "$slug" --as "$name" 2>&1)"
if ! grep -q "stream.sh" <<<"$join_out"; then
  echo "FAIL: join.sh output missing stream.sh invocation hint." >&2
  echo "----- output -----" >&2
  echo "$join_out" >&2
  exit 2
fi

# --- Send a message ---

echo "Sending message..."
bash "$skill_dir/send.sh" "$slug" --as "$peer" <<<"$body"

# --- Read it back ---

echo "Reading history..."
history_out="$(bash "$skill_dir/history.sh" "$slug" 2>&1)"
if ! grep -qF "$body" <<<"$history_out"; then
  echo "FAIL: history.sh did not contain the sent body." >&2
  echo "----- history -----" >&2
  echo "$history_out" >&2
  exit 2
fi
if ! grep -qF "$peer" <<<"$history_out"; then
  echo "FAIL: history.sh did not record the sender." >&2
  echo "----- history -----" >&2
  echo "$history_out" >&2
  exit 2
fi

# --- Leave-on-teardown: stream.sh should emit a "leave" event when signalled ---

echo "Testing leave-on-teardown..."
bash "$skill_dir/stream.sh" "$slug" "$name" >/dev/null 2>&1 &
stream_pid=$!
# Give it a moment to install its signal trap and start tailing.
sleep 1
kill -TERM "$stream_pid"
wait "$stream_pid" 2>/dev/null || true

leave_out="$(bash "$skill_dir/history.sh" "$slug" 2>&1)"
if ! grep -qF "left channel" <<<"$leave_out"; then
  echo "FAIL: stream.sh did not emit a leave event on SIGTERM." >&2
  echo "----- history -----" >&2
  echo "$leave_out" >&2
  exit 2
fi

echo "PASS: join/send/history round-trip + leave-on-teardown succeeded."
