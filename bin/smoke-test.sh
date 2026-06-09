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
#
# Deliberately install OUTSIDE $HOME and under a path containing a space. This
# makes every assertion below run through a relocated install: it proves the
# scripts self-resolve their own dir (BASH_SOURCE) from any location, and that
# a spaced install path (realistic for a project install like
# "~/My Projects/...") doesn't break the printed Monitor command. Channel state
# still lives under $HOME, which the state-separation check below verifies.

tmphome="$(mktemp -d)"
tmproot="$(mktemp -d)"
trap 'rm -rf "$tmphome" "$tmproot"' EXIT
export HOME="$tmphome"

skill_dir="$tmproot/My Claude Skills/agent-chat"
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
# The Monitor command must point at THIS install's stream.sh by its resolved
# absolute path — not a hardcoded ~/.claude path — so project-level installs
# stream from their own copy.
if ! grep -qF "bash \"$skill_dir/stream.sh\"" <<<"$join_out"; then
  echo "FAIL: join.sh Monitor command did not reference \"$skill_dir/stream.sh\" (quoted)." >&2
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

# --- Relocatability: a non-$HOME, spaced install still puts channel state under
# $HOME (shared rendezvous), never beside the scripts. The round-trip above
# already proved the scripts RUN from $skill_dir; here we prove the split. ---

echo "Verifying state lands under \$HOME, not the install dir..."
if [[ ! -f "$HOME/.claude/agent-chat/$slug/log" ]]; then
  echo "FAIL: channel log is not under \$HOME/.claude/agent-chat/$slug/." >&2
  exit 2
fi
if find "$tmproot" -name log -type f 2>/dev/null | grep -q .; then
  echo "FAIL: channel state leaked into the install tree ($tmproot)." >&2
  find "$tmproot" -name log -type f >&2
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

# --- Stale-peer reaping: a live agent posts a leave for a peer that was hard-
# killed (no graceful trap), detected via its aged-out presence file. ---

echo "Testing stale-peer reaping..."
presence_dir="$HOME/.claude/agent-chat/$slug/presence"
mkdir -p "$presence_dir"
ghost="smoke-ghost"
# Plant a presence file and backdate it well past the staleness window.
# `touch -t` reads its stamp in LOCAL time, so compute the stamp in local time
# too (no -u) or the backdate can land in the future on a non-UTC machine.
touch "$presence_dir/$ghost"
stale_ts="$(date -v-10M +%Y%m%d%H%M 2>/dev/null || date -d '10 minutes ago' +%Y%m%d%H%M)"
touch -t "$stale_ts" "$presence_dir/$ghost"

# A send by a live agent triggers a reap of the stale ghost.
bash "$skill_dir/send.sh" "$slug" --as "$name" <<<"trigger reap" >/dev/null

reap_out="$(bash "$skill_dir/history.sh" "$slug" 2>&1)"
if ! grep -qF "$ghost" <<<"$reap_out" || ! grep -qF "timed out" <<<"$reap_out"; then
  echo "FAIL: stale peer '$ghost' was not reaped with a leave event." >&2
  echo "----- history -----" >&2
  echo "$reap_out" >&2
  exit 2
fi
if [[ -e "$presence_dir/$ghost" ]]; then
  echo "FAIL: reaped peer's presence file was not removed." >&2
  exit 2
fi

# Reaping must be one-shot: a second send must not post a duplicate leave.
before="$(grep -cF "$ghost" <<<"$reap_out")"
bash "$skill_dir/send.sh" "$slug" --as "$name" <<<"second trigger" >/dev/null
after="$(bash "$skill_dir/history.sh" "$slug" 2>&1 | grep -cF "$ghost")"
if [[ "$before" != "$after" ]]; then
  echo "FAIL: stale peer reaped more than once ($before -> $after leave lines)." >&2
  exit 2
fi

# --- Mention resolution: an @token addresses only a present member; an
# unrecognized token (scoped package, typo) falls back to broadcast rather than
# silently narrowing the message to a phantom peer. ---

echo "Testing mention resolution against the roster..."
mslug="smoke-mentions-$$"
mlog="$HOME/.claude/agent-chat/$mslug/log"
mpresence="$HOME/.claude/agent-chat/$mslug/presence"
# Create the channel, then a two-member roster (alice, bob) via presence files.
bash "$skill_dir/join.sh" "$mslug" --as alice >/dev/null
mkdir -p "$mpresence"
touch "$mpresence/alice" "$mpresence/bob"

# A known member plus a scoped-package token: only the member is addressed.
bash "$skill_dir/send.sh" "$mslug" --as alice <<<"ping @bob and check @vercel/otel" >/dev/null
got="$(tail -n1 "$mlog" | jq -c '.mentions')"
if [[ "$got" != '["bob"]' ]]; then
  echo "FAIL: expected mentions [\"bob\"], got $got (a scoped package must not register as a mention)." >&2
  exit 2
fi

# Only an unknown token: no member named 'vercel', so the message broadcasts.
bash "$skill_dir/send.sh" "$mslug" --as alice <<<"heads up: @vercel/otel changed behavior" >/dev/null
got="$(tail -n1 "$mlog" | jq -c '.mentions')"
if [[ "$got" != '[]' ]]; then
  echo "FAIL: expected empty mentions (broadcast) for an unrecognized @token, got $got." >&2
  exit 2
fi

echo "PASS: relocated/spaced install + join/send/history round-trip + leave-on-teardown + stale-peer reaping + mention resolution succeeded."
