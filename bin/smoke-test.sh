#!/usr/bin/env bash
#
# smoke-test.sh — end-to-end check of the agent-chat skill.
#
# Exercises join, send, history, and stream (via the shell shims, which
# delegate to the agent-chat binary) against a throwaway channel under a temp
# HOME so it can't collide with real chat state. Doesn't touch Monitor — that's
# a Claude Code primitive, not something we can drive from a shell.
#
# Usage:
#   bin/smoke-test.sh
#
# Exit codes: 0 = pass, 1 = setup/precondition failure, 2 = behavior mismatch.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# --- Preconditions ---

if ! command -v go >/dev/null 2>&1; then
  echo "FAIL: 'go' not on PATH — install Go (https://go.dev/dl/) and retry." >&2
  exit 1
fi

# --- Build binary ---

echo "Building agent-chat binary..."
go build -o cmd/agent-chat/agent-chat ./cmd/agent-chat/ \
  || { echo "FAIL: go build failed." >&2; exit 1; }

# --- Install into an isolated HOME ---
#
# Deliberately install OUTSIDE $HOME and under a path containing a space. This
# makes every assertion below run through a relocated install: it proves the
# binary and shims self-resolve their own dir from any location, and that a
# spaced install path doesn't break the printed Monitor command. Channel state
# still lives under $HOME, which the state-separation check below verifies.

tmphome="$(mktemp -d)"
tmproot="$(mktemp -d)"
trap 'rm -rf "$tmphome" "$tmproot"' EXIT
export HOME="$tmphome"

export AGENT_CHAT_NO_UPDATE_CHECK=1

skill_dir="$tmproot/My Claude Skills/agent-chat"
mkdir -p "$skill_dir"
cp -R skill/. "$skill_dir/"
cp cmd/agent-chat/agent-chat "$skill_dir/agent-chat"
chmod +x "$skill_dir"/*.sh "$skill_dir/agent-chat"
echo "Installed skill -> $skill_dir"

slug="smoke-$$"
name="smoke-tester"
peer="smoke-peer"
body="ping from smoke test $$"

# --- Join (creates the channel) ---

echo "Joining channel $slug as $name..."
join_out="$(bash "$skill_dir/join.sh" "$slug" --as "$name" 2>&1)"

# The Monitor command must reference stream.sh inside THIS install's dir.
if ! grep -qF "$skill_dir/stream.sh" <<<"$join_out"; then
  echo "FAIL: join Monitor command did not reference $skill_dir/stream.sh" >&2
  echo "----- output -----" >&2
  echo "$join_out" >&2
  exit 2
fi

# --- Send a message ---

echo "Sending message..."
bash "$skill_dir/send.sh" "$slug" --as "$peer" <<<"@all $body"

# --- Read it back ---

echo "Reading history..."
history_out="$(bash "$skill_dir/history.sh" "$slug" 2>&1)"
if ! grep -qF "$body" <<<"$history_out"; then
  echo "FAIL: history did not contain the sent body." >&2
  echo "----- history -----" >&2; echo "$history_out" >&2; exit 2
fi
if ! grep -qF "$peer" <<<"$history_out"; then
  echo "FAIL: history did not record the sender." >&2
  echo "----- history -----" >&2; echo "$history_out" >&2; exit 2
fi

# --- Relocatability: state must land under $HOME, not the install dir ---

echo "Verifying state lands under \$HOME, not the install dir..."
if [[ ! -f "$HOME/.claude/agent-chat/$slug/log" ]]; then
  echo "FAIL: channel log is not under \$HOME/.claude/agent-chat/$slug/." >&2; exit 2
fi
if find "$tmproot" -name log -type f 2>/dev/null | grep -q .; then
  echo "FAIL: channel state leaked into the install tree ($tmproot)." >&2
  find "$tmproot" -name log -type f >&2; exit 2
fi

# --- Leave-on-teardown: stream must emit a "leave" when signalled ---

echo "Testing leave-on-teardown..."
bash "$skill_dir/stream.sh" "$slug" "$name" >/dev/null 2>&1 &
stream_pid=$!
sleep 1
kill -TERM "$stream_pid"
wait "$stream_pid" 2>/dev/null || true

leave_out="$(bash "$skill_dir/history.sh" "$slug" 2>&1)"
if ! grep -qF "left channel" <<<"$leave_out"; then
  echo "FAIL: stream did not emit a leave event on SIGTERM." >&2
  echo "----- history -----" >&2; echo "$leave_out" >&2; exit 2
fi

# --- Stale-peer reaping (via the stream heartbeat, the real reaper) ---
#
# Reaping is the long-running stream's job, not a one-shot send's: a fresh CLI
# process has no tick history, so it can't tell a genuinely stale peer from one
# that only looks stale because the host just woke from sleep (issue #39). Drive
# a short-lived stream with a fast heartbeat so it reaps the planted ghost on an
# early tick; its several ticks also prove the reap is one-shot.

echo "Testing stale-peer reaping..."
presence_dir="$HOME/.claude/agent-chat/$slug/presence"
mkdir -p "$presence_dir"
ghost="smoke-ghost"
touch "$presence_dir/$ghost"
stale_ts="$(date -v-10M +%Y%m%d%H%M 2>/dev/null || date -d '10 minutes ago' +%Y%m%d%H%M)"
touch -t "$stale_ts" "$presence_dir/$ghost"

AGENT_CHAT_STALE_SECS=30 AGENT_CHAT_HEARTBEAT_SECS=1 \
  bash "$skill_dir/stream.sh" "$slug" "$name" >/dev/null 2>&1 &
reaper_pid=$!
sleep 3 # a few heartbeat ticks
kill -TERM "$reaper_pid" 2>/dev/null || true
wait "$reaper_pid" 2>/dev/null || true

reap_out="$(bash "$skill_dir/history.sh" "$slug" 2>&1)"
if ! grep -qF "$ghost" <<<"$reap_out" || ! grep -qF "timed out" <<<"$reap_out"; then
  echo "FAIL: stale peer '$ghost' was not reaped with a leave event." >&2
  echo "----- history -----" >&2; echo "$reap_out" >&2; exit 2
fi
if [[ -e "$presence_dir/$ghost" ]]; then
  echo "FAIL: reaped peer's presence file was not removed." >&2; exit 2
fi

# Reaping must be one-shot: exactly one [leave] for the ghost despite several
# heartbeat ticks over the stream's lifetime.
ghost_leaves_after_reap="$(grep -F "$ghost" <<<"$reap_out" | grep -cF "[leave]" || true)"
if [[ "$ghost_leaves_after_reap" -ne 1 ]]; then
  echo "FAIL: stale peer reaped $ghost_leaves_after_reap times, expected exactly 1." >&2
  echo "----- history -----" >&2; echo "$reap_out" >&2; exit 2
fi

# A send by a reaped member must NOT resurrect its presence (refresh-only), so
# the sweep can't re-announce the same departure with no join (issue #29).
bash "$skill_dir/send.sh" "$slug" --as "$ghost" <<<"@all ghost tries to speak" >/dev/null
if [[ -e "$presence_dir/$ghost" ]]; then
  echo "FAIL: a send resurrected reaped peer '$ghost' presence (should require re-join)." >&2; exit 2
fi
# No new leave for the ghost: exactly one [leave] header line, ever (the ghost's
# own msg is fine — sends are allowed; only presence resurrection is forbidden).
ghost_leaves="$(bash "$skill_dir/history.sh" "$slug" 2>&1 | grep -F "$ghost" | grep -cF "[leave]" || true)"
if [[ "$ghost_leaves" -ne 1 ]]; then
  echo "FAIL: expected exactly 1 leave for reaped peer '$ghost', got $ghost_leaves." >&2; exit 2
fi

# --- Mention resolution ---

echo "Testing mention resolution against the roster..."
mslug="smoke-mentions-$$"
mlog="$HOME/.claude/agent-chat/$mslug/log"
mpresence="$HOME/.claude/agent-chat/$mslug/presence"

bash "$skill_dir/join.sh" "$mslug" --as alice >/dev/null
mkdir -p "$mpresence"
touch "$mpresence/alice" "$mpresence/bob"

# Known member + scoped-package token: only the member is addressed.
bash "$skill_dir/send.sh" "$mslug" --as alice <<<"ping @bob and check @vercel/otel" >/dev/null
# Read mentions directly from the raw JSONL log line via the binary.
got_mentions="$(tail -n1 "$mlog" | python3 -c 'import sys,json; r=json.load(sys.stdin); print(json.dumps(r.get("mentions",[])))')"
if [[ "$got_mentions" != '["bob"]' ]]; then
  echo "FAIL: expected mentions [\"bob\"], got $got_mentions" >&2; exit 2
fi

# Unknown token only (no present member, no @all): refused, not broadcast.
# A prose @token like @vercel/otel must not silently spray the channel — the
# sender has to say @all to broadcast (ADR-0010).
if bash "$skill_dir/send.sh" "$mslug" --as alice <<<"heads up: @vercel/otel changed" >/dev/null 2>&1; then
  echo "FAIL: send with only an unrecognized @token should be refused (use @all to broadcast)." >&2; exit 2
fi

# --- Feedback poll round (open -> submit x2 -> tally -> close) ---

echo "Testing feedback poll round..."
fslug="smoke-feedback-$$"

# A fresh channel has no open round: submit/tally must refuse.
if bash "$skill_dir/feedback.sh" tally "$fslug" >/dev/null 2>&1; then
  echo "FAIL: tally succeeded with no open round." >&2; exit 2
fi

bash "$skill_dir/feedback.sh" open "$fslug" --as opener >/dev/null

# Two members submit; one item is a near-duplicate (case + spacing) of another.
bash "$skill_dir/feedback.sh" submit "$fslug" --as opener <<<"mentions are confusing" >/dev/null
bash "$skill_dir/feedback.sh" submit "$fslug" --as worker <<'EOF' >/dev/null
join output too long
Mentions are  Confusing
EOF

tally_out="$(bash "$skill_dir/feedback.sh" tally "$fslug" 2>&1)"
if ! grep -qF "mentions are confusing" <<<"$tally_out" || ! grep -qF "join output too long" <<<"$tally_out"; then
  echo "FAIL: tally missing expected candidate items." >&2
  echo "----- tally -----" >&2; echo "$tally_out" >&2; exit 2
fi
# The duplicate must collapse: exactly 2 numbered candidates.
tally_count="$(grep -cE '^  [0-9]+\. ' <<<"$tally_out" || true)"
if [[ "$tally_count" -ne 2 ]]; then
  echo "FAIL: expected 2 deduped candidates, got $tally_count." >&2
  echo "----- tally -----" >&2; echo "$tally_out" >&2; exit 2
fi

# A second open while one is live must be refused.
if bash "$skill_dir/feedback.sh" open "$fslug" --as opener >/dev/null 2>&1; then
  echo "FAIL: opened a second round while one was already live." >&2; exit 2
fi

bash "$skill_dir/feedback.sh" close "$fslug" --as opener --outcome filed >/dev/null

# After close, tally/submit see the round as closed.
if bash "$skill_dir/feedback.sh" tally "$fslug" >/dev/null 2>&1; then
  echo "FAIL: tally succeeded after the round was closed." >&2; exit 2
fi

# --- Feedback poll trigger on join (#33) ---

echo "Testing feedback poll trigger on join..."

# RATE=1 forces the channel-creating join to open a round, and its output must
# nudge the agent to submit.
tslug="smoke-trigger-$$"
trig_out="$(AGENT_CHAT_FEEDBACK_RATE=1 bash "$skill_dir/join.sh" "$tslug" --as first 2>&1)"
if ! grep -qF "feedback round is open" <<<"$trig_out"; then
  echo "FAIL: forced-rate join did not nudge about the open round." >&2
  echo "----- join output -----" >&2; echo "$trig_out" >&2; exit 2
fi
if ! bash "$skill_dir/feedback.sh" tally "$tslug" >/dev/null 2>&1; then
  echo "FAIL: no round open after a forced-rate creating join." >&2; exit 2
fi

# A second join (also forced) must NOT open a second round: exactly one poll-open.
AGENT_CHAT_FEEDBACK_RATE=1 bash "$skill_dir/join.sh" "$tslug" --as second >/dev/null 2>&1
opens="$(bash "$skill_dir/history.sh" "$tslug" 2>&1 | grep -cF "[poll-open]" || true)"
if [[ "$opens" -ne 1 ]]; then
  echo "FAIL: expected exactly 1 poll-open after two joins, got $opens." >&2; exit 2
fi

# RATE=0 disables entirely: a creating join opens no round and gives no nudge.
zslug="smoke-notrigger-$$"
noz_out="$(AGENT_CHAT_FEEDBACK_RATE=0 bash "$skill_dir/join.sh" "$zslug" --as solo 2>&1)"
if grep -qF "feedback round is open" <<<"$noz_out"; then
  echo "FAIL: rate=0 join still nudged about a round." >&2; exit 2
fi
if bash "$skill_dir/feedback.sh" tally "$zslug" >/dev/null 2>&1; then
  echo "FAIL: rate=0 join opened a round." >&2; exit 2
fi

echo "PASS: build + relocated/spaced install + join/send/history round-trip + leave-on-teardown + stale-peer reaping + mention resolution + feedback poll round + join trigger."
