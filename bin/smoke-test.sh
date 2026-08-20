#!/usr/bin/env bash
#
# smoke-test.sh — end-to-end check of the agent-chat skill.
#
# Exercises join, send, history, and the doorbell (via the shell shims, which
# delegate to the agent-chat binary) against a throwaway channel under a temp
# HOME so it can't collide with real chat state.
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
# spaced install path doesn't break the printed doorbell command. Channel state
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
# Run from a neutral cwd: hookInstalled() also consults the PROJECT's
# .claude/settings.json at the git root of the joining cwd, and this repo's may
# legitimately carry the hook — which would flip this join to the subscribed
# story and break the not-subscribed assertions below.
join_out="$(cd "$tmphome" && CLAUDE_CODE_SESSION_ID="smoke-early-$$" bash "$skill_dir/join.sh" "$slug" --as "$name" 2>&1)"

# A hook-less Claude Code session gets no delivery at all, and must say so
# rather than hand out a doorbell that would wake the agent with nothing to read.
if ! grep -q "NOT SUBSCRIBED" <<<"$join_out" || ! grep -q "hook install" <<<"$join_out"; then
  echo "FAIL: hook-less join did not report the missing hook and nudge 'hook install'." >&2
  echo "----- output -----" >&2; echo "$join_out" >&2; exit 2
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

# --- Stale-peer reaping (via the doorbell heartbeat, the real reaper) ---
#
# Reaping is a long-running process's job, not a one-shot send's: a fresh CLI
# process has no tick history, so it can't tell a genuinely stale peer from one
# that only looks stale because the host just woke from sleep (issue #39). Drive
# a short-lived doorbell with a fast heartbeat so it reaps the planted ghost on
# an early tick; its several ticks also prove the reap is one-shot.

echo "Testing stale-peer reaping..."
presence_dir="$HOME/.claude/agent-chat/$slug/presence"
mkdir -p "$presence_dir"
ghost="smoke-ghost"
touch "$presence_dir/$ghost"
stale_ts="$(date -v-10M +%Y%m%d%H%M 2>/dev/null || date -d '10 minutes ago' +%Y%m%d%H%M)"
touch -t "$stale_ts" "$presence_dir/$ghost"

# Directed message to the ghost that it will never read (it isn't streaming, so
# it has no doorbell of its own). When the ghost is reaped below, this must bounce back
# to the sender ($name, who IS present as the reaping doorbell) — a departed peer's
# unread directed message fails loudly instead of vanishing (ADR-0011).
bounce_body="did you get this ghost? $$"
bash "$skill_dir/send.sh" "$slug" --as "$name" <<<"@$ghost $bounce_body" >/dev/null

AGENT_CHAT_STALE_SECS=30 AGENT_CHAT_HEARTBEAT_SECS=1 \
  bash "$skill_dir/wait.sh" "$slug" "$name" >/dev/null 2>&1 &
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
# heartbeat ticks over the doorbell's lifetime.
ghost_leaves_after_reap="$(grep -F "$ghost" <<<"$reap_out" | grep -cF "[leave]" || true)"
if [[ "$ghost_leaves_after_reap" -ne 1 ]]; then
  echo "FAIL: stale peer reaped $ghost_leaves_after_reap times, expected exactly 1." >&2
  echo "----- history -----" >&2; echo "$reap_out" >&2; exit 2
fi

# The ghost's unread directed message must have bounced: a [bounce] record that
# echoes the original body. Broadcasts don't bounce; a directed hand-off does.
if ! grep -qF "[bounce]" <<<"$reap_out"; then
  echo "FAIL: no [bounce] for the ghost's unread directed message." >&2
  echo "----- history -----" >&2; echo "$reap_out" >&2; exit 2
fi
if ! grep -qF "$bounce_body" <<<"$reap_out"; then
  echo "FAIL: the bounce did not echo the undelivered message body." >&2
  echo "----- history -----" >&2; echo "$reap_out" >&2; exit 2
fi
# Exactly one bounce (per undelivered directed message) — no cascade/storm.
bounce_count="$(grep -cF "[bounce]" <<<"$reap_out" || true)"
if [[ "$bounce_count" -ne 1 ]]; then
  echo "FAIL: expected exactly 1 bounce, got $bounce_count." >&2
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
# A prose @token like @vercel/otel must not silently spray the channel — an
# @-token that matches nobody is treated as a mistake (ADR-0010).
if bash "$skill_dir/send.sh" "$mslug" --as alice <<<"heads up: @vercel/otel changed" >/dev/null 2>&1; then
  echo "FAIL: send with only an unrecognized @token should be refused (use @all to broadcast)." >&2; exit 2
fi

# No @-mention at all: accepted as a pull-only FYI (ADR-0012), NOT refused. It
# stores as kind=fyi and is visible on pull (history), the deliberate-quiet tier.
echo "Testing pull-only FYI tier..."
fyi_note="just a breadcrumb, nobody needs waking $$"
if ! bash "$skill_dir/send.sh" "$mslug" --as alice <<<"$fyi_note" >/dev/null 2>&1; then
  echo "FAIL: an unaddressed send should be accepted as an FYI, not refused." >&2; exit 2
fi
got_kind="$(tail -n1 "$mlog" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("kind",""))')"
if [[ "$got_kind" != "fyi" ]]; then
  echo "FAIL: unaddressed send stored as kind='$got_kind', want 'fyi'." >&2; exit 2
fi
if ! bash "$skill_dir/history.sh" "$mslug" 2>&1 | grep -qF "$fyi_note"; then
  echo "FAIL: FYI note not visible in history (it should be pullable)." >&2; exit 2
fi

# An FYI must never be a wake event; a directed message must be. The doorbell
# is the wake primitive (it exits to wake an idle agent), so arm one and watch
# what it does: still blocked after an FYI, exited after a directed message.
echo "Testing FYI is not a wake event (doorbell ignores FYI, wakes on directed)..."
bash "$skill_dir/join.sh" "$mslug" --as watcher >/dev/null
AGENT_CHAT_SIGNAL_GRACE_MS=200 bash "$skill_dir/wait.sh" "$mslug" watcher >/dev/null 2>&1 &
watch_pid=$!
sleep 1 # let the doorbell arm and seed its cursor at the current log end
bash "$skill_dir/send.sh" "$mslug" --as alice <<<"FYISENTINEL-stay-quiet-$$" >/dev/null
sleep 1
if ! kill -0 "$watch_pid" 2>/dev/null; then
  echo "FAIL: doorbell exited on an FYI (it must be pull-only, never a wake event)." >&2; exit 2
fi
bash "$skill_dir/send.sh" "$mslug" --as alice <<<"@watcher DIRECTEDSENTINEL-$$" >/dev/null
if ! wait "$watch_pid"; then
  echo "FAIL: doorbell did not wake on a directed @watcher message." >&2; exit 2
fi

# --- Hook-based delivery (#59) ---
#
# Runs last on purpose: it writes $HOME/.claude/settings.json, and every
# assertion above must see the hook-free world (the not-subscribed join story).

echo "Testing hook install (idempotent settings.json merge)..."
hbin="$skill_dir/agent-chat"
mkdir -p "$HOME/.claude"
cat > "$HOME/.claude/settings.json" <<'EOF'
{"model":"opus","hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"other-tool guard"}]}]}}
EOF
"$hbin" hook install >/dev/null
python3 - "$HOME/.claude/settings.json" <<'PY'
import json, sys
s = json.load(open(sys.argv[1]))
assert s["model"] == "opus", "foreign top-level key lost"
pre = json.dumps(s["hooks"]["PreToolUse"])
assert "other-tool guard" in pre, "foreign hook lost"
for ev in ("PostToolUse", "UserPromptSubmit", "SessionEnd"):
    flat = json.dumps(s["hooks"].get(ev, []))
    assert 'exec \\"$BIN\\" hook' in flat or 'exec "$BIN" hook' in flat, f"agent-chat hook missing from {ev}"
PY
if ! "$hbin" hook install | grep -q "already registered"; then
  echo "FAIL: second hook install was not a no-op." >&2; exit 2
fi

echo "Testing hook-based delivery..."
hslug="smoke-hook-$$"
hsid="smoke-sid-$$"

# A join inside a hook-installed session registers itself and says so.
hjoin_out="$(CLAUDE_CODE_SESSION_ID="$hsid" bash "$skill_dir/join.sh" "$hslug" --as hooked 2>&1)"
if ! grep -q "SUBSCRIBED" <<<"$hjoin_out"; then
  echo "FAIL: hook-installed join did not print the subscribed story." >&2
  echo "----- join output -----" >&2; echo "$hjoin_out" >&2; exit 2
fi
if [[ ! -f "$HOME/.claude/agent-chat/sessions/$hsid" ]]; then
  echo "FAIL: join did not register the session." >&2; exit 2
fi

# A peer's send arrives on the next hook fire as additionalContext — and only once.
bash "$skill_dir/send.sh" "$hslug" --as hookpeer <<<"@all URGENT-STOP-$$" >/dev/null
fire() { printf '{"session_id":"%s","hook_event_name":"PostToolUse"}' "$1" | "$hbin" hook; }
hook_out="$(fire "$hsid")"
if ! grep -qF "URGENT-STOP-$$" <<<"$hook_out" || ! grep -qF '"additionalContext"' <<<"$hook_out"; then
  echo "FAIL: hook fire did not deliver the peer message as additionalContext." >&2
  echo "----- hook output -----" >&2; echo "$hook_out" >&2; exit 2
fi
if [[ -n "$(fire "$hsid")" ]]; then
  echo "FAIL: second hook fire re-delivered (frontier did not advance)." >&2; exit 2
fi

# A session that never joined pays nothing and prints nothing.
if [[ -n "$(fire "never-joined-$$")" ]]; then
  echo "FAIL: non-member session got hook output." >&2; exit 2
fi

# SessionEnd posts the clean leave and deregisters.
printf '{"session_id":"%s","hook_event_name":"SessionEnd"}' "$hsid" | "$hbin" hook
if ! bash "$skill_dir/history.sh" "$hslug" 2>&1 | grep -F "hooked" | grep -qF "[leave]"; then
  echo "FAIL: SessionEnd did not post a leave for the hook subscriber." >&2; exit 2
fi
if [[ -e "$HOME/.claude/agent-chat/sessions/$hsid" ]]; then
  echo "FAIL: SessionEnd did not deregister the session." >&2; exit 2
fi

# --- Signal-mode doorbell (#60) ---
#
# The doorbell exits empty on wake-worthy traffic (never delivering, never
# moving the frontier), the hook then delivers on the next fire, and a dead
# doorbell earns a re-arm nag from the hook until re-armed or opted out.

echo "Testing signal-mode doorbell..."
dsid="smoke-doorbell-sid-$$"
CLAUDE_CODE_SESSION_ID="$dsid" bash "$skill_dir/join.sh" "$hslug" --as dozer >/dev/null
dz_cursor="$HOME/.claude/agent-chat/$hslug/cursors/dozer"
dz_seed="$(cat "$dz_cursor")"

dbell_out="$(mktemp)"
AGENT_CHAT_SIGNAL_GRACE_MS=200 bash "$skill_dir/wait.sh" "$hslug" dozer >"$dbell_out" 2>&1 &
dbell_pid=$!
sleep 1
bash "$skill_dir/send.sh" "$hslug" --as hookpeer <<<"@all KNOCK-$$" >/dev/null
if ! wait "$dbell_pid"; then
  echo "FAIL: signal doorbell exited nonzero." >&2; cat "$dbell_out" >&2; exit 2
fi
if ! grep -q "make any tool call" "$dbell_out" || grep -qF "KNOCK-$$" "$dbell_out"; then
  echo "FAIL: doorbell should signal without delivering content." >&2
  cat "$dbell_out" >&2; exit 2
fi
rm -f "$dbell_out"
if [[ "$(cat "$dz_cursor")" != "$dz_seed" ]]; then
  echo "FAIL: doorbell moved the read frontier (that's the hook's job)." >&2; exit 2
fi

# The next hook fire delivers the message AND nags about the now-dead doorbell.
dz_fire="$(fire "$dsid")"
if ! grep -qF "KNOCK-$$" <<<"$dz_fire"; then
  echo "FAIL: hook did not deliver the doorbell's message." >&2
  echo "$dz_fire" >&2; exit 2
fi
if ! grep -q "doorbell" <<<"$dz_fire" || ! grep -q "wait" <<<"$dz_fire"; then
  echo "FAIL: hook did not nag about the dead doorbell with a re-arm command." >&2
  echo "$dz_fire" >&2; exit 2
fi
# Opting out (deleting the lockfile) silences the nag.
rm -f "$HOME/.claude/agent-chat/doorbells/$hslug--dozer.lock"
if [[ -n "$(fire "$dsid")" ]]; then
  echo "FAIL: hook fire not silent after doorbell opt-out with no traffic." >&2; exit 2
fi

# Deliberate mid-session leave: posts the [leave], deregisters, hook goes quiet.
echo "Testing deliberate leave..."
lsid="smoke-leave-sid-$$"
CLAUDE_CODE_SESSION_ID="$lsid" bash "$skill_dir/join.sh" "$hslug" --as leaver >/dev/null
if [[ ! -f "$HOME/.claude/agent-chat/sessions/$lsid" ]]; then
  echo "FAIL: leaver join did not register." >&2; exit 2
fi
CLAUDE_CODE_SESSION_ID="$lsid" bash "$skill_dir/leave.sh" "$hslug" --as leaver >/dev/null
if ! bash "$skill_dir/history.sh" "$hslug" 2>&1 | grep -F "leaver" | grep -qF "[leave]"; then
  echo "FAIL: leave.sh did not post a leave record." >&2; exit 2
fi
if [[ -e "$HOME/.claude/agent-chat/sessions/$lsid" ]]; then
  echo "FAIL: leave.sh did not deregister the session." >&2; exit 2
fi
if [[ -e "$HOME/.claude/agent-chat/$hslug/presence/leaver" ]]; then
  echo "FAIL: leave.sh did not remove presence." >&2; exit 2
fi
bash "$skill_dir/send.sh" "$hslug" --as hookpeer <<<"@all after-leave $$" >/dev/null
if [[ -n "$(fire "$lsid")" ]]; then
  echo "FAIL: hook still delivered to a departed session." >&2; exit 2
fi

echo "PASS: build + relocated/spaced install + join/send/history round-trip + stale-peer reaping + undeliverable bounce + mention resolution + pull-only FYI + hook install/delivery + signal doorbell + deliberate leave."
