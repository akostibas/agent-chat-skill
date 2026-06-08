#!/bin/bash
# Shared helpers for agent-chat scripts. Source, don't execute.

set -euo pipefail

AGENT_CHAT_ROOT="${AGENT_CHAT_ROOT:-$HOME/.claude/agent-chat}"
AGENT_CHAT_TTL_DAYS="${AGENT_CHAT_TTL_DAYS:-14}"
# Liveness: a streaming agent refreshes its presence file every
# HEARTBEAT_SECS; a peer is declared gone (and a leave emitted on its behalf)
# once its presence is older than STALE_SECS. STALE must be a comfortable
# multiple of HEARTBEAT so a momentarily busy agent isn't reaped early.
AGENT_CHAT_HEARTBEAT_SECS="${AGENT_CHAT_HEARTBEAT_SECS:-15}"
AGENT_CHAT_STALE_SECS="${AGENT_CHAT_STALE_SECS:-45}"
IDENT_RE='^[a-zA-Z0-9_-]{1,40}$'

die() { echo "agent-chat: $*" >&2; exit 1; }

validate_ident() {
  local kind="$1" value="$2"
  [[ "$value" =~ $IDENT_RE ]] || die "invalid $kind '$value' (must match $IDENT_RE)"
}

channel_dir() { echo "$AGENT_CHAT_ROOT/$1"; }
channel_log() { echo "$AGENT_CHAT_ROOT/$1/log"; }
channel_lock() { echo "$AGENT_CHAT_ROOT/$1/log.lock"; }
presence_dir() { echo "$AGENT_CHAT_ROOT/$1/presence"; }
presence_file() { echo "$AGENT_CHAT_ROOT/$1/presence/$2"; }

ensure_channel() {
  local slug="$1"
  mkdir -p "$(channel_dir "$slug")"
  touch "$(channel_log "$slug")"
}

# Acquire shlock with retry. Args: lockfile, timeout_seconds.
acquire_lock() {
  local lockfile="$1" timeout="${2:-5}"
  local deadline=$(( $(date +%s) + timeout ))
  while ! shlock -p $$ -f "$lockfile" 2>/dev/null; do
    if [[ $(date +%s) -ge $deadline ]]; then
      die "could not acquire lock $lockfile within ${timeout}s (held by PID $(cat "$lockfile" 2>/dev/null || echo '?'))"
    fi
    sleep 0.05
  done
}

release_lock() {
  local lockfile="$1"
  rm -f "$lockfile"
}

iso_now() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }

# Epoch mtime of a file, portable across BSD (macOS) and GNU stat. Empty on
# error (missing file, etc.).
file_mtime() { stat -f %m "$1" 2>/dev/null || stat -c %Y "$1" 2>/dev/null || true; }

# Refresh this agent's presence marker — proof of life for peers' reapers.
# Best-effort: a failure here must never break streaming.
touch_presence() {
  local slug="$1" name="$2" pdir
  pdir="$(presence_dir "$slug")"
  mkdir -p "$pdir" 2>/dev/null || return 0
  touch "$pdir/$name" 2>/dev/null || true
}

# Append a leave event for $name to the channel log, under lock. Used both for
# an agent's own graceful departure and for a peer reaped on its behalf. The
# $body distinguishes the two in the log without changing the kind peers see.
emit_leave_event() {
  local slug="$1" name="$2" body="${3:-left channel}" log lock
  log="$(channel_log "$slug")"
  lock="$(channel_lock "$slug")"
  (
    acquire_lock "$lock" 2
    jq -nc --arg ts "$(iso_now)" --arg sender "$name" --arg body "$body" \
      '{ts:$ts, sender:$sender, kind:"leave", body:$body}' >> "$log"
    release_lock "$lock"
  ) 2>/dev/null || true
}

# A peer's session can vanish without warning — Claude Code's Monitor hard-
# kills (SIGKILL) the stream child on session close, so no in-process trap can
# announce the departure (see ADR-0003). Instead, every live agent reaps: scan
# peers' presence files and, for any whose heartbeat is older than STALE_SECS,
# emit a leave on its behalf. The reap is claimed under the channel lock by
# removing the presence file before posting, so concurrent reapers can't
# double-post: whoever deletes the file first is the one that announces.
reap_stale_peers() {
  local slug="$1" me="$2" pdir lock now name mt mt2
  pdir="$(presence_dir "$slug")"
  [[ -d "$pdir" ]] || return 0
  lock="$(channel_lock "$slug")"
  now="$(date +%s)"
  local f
  for f in "$pdir"/*; do
    [[ -e "$f" ]] || continue          # empty glob
    name="$(basename "$f")"
    [[ "$name" == "$me" ]] && continue # never reap myself
    mt="$(file_mtime "$f")"
    [[ -n "$mt" ]] || continue
    (( now - mt > AGENT_CHAT_STALE_SECS )) || continue
    (
      acquire_lock "$lock" 2
      # Re-check under lock: another reaper may have claimed it, or the peer
      # may have come back, between the unlocked scan and now.
      if [[ -e "$f" ]]; then
        mt2="$(file_mtime "$f")"
        if [[ -n "$mt2" ]] && (( now - mt2 > AGENT_CHAT_STALE_SECS )); then
          rm -f "$f"
          jq -nc --arg ts "$(iso_now)" --arg sender "$name" \
            '{ts:$ts, sender:$sender, kind:"leave", body:"left channel (timed out)"}' \
            >> "$(channel_log "$slug")"
        fi
      fi
      release_lock "$lock"
    ) 2>/dev/null || true
  done
}

# Worktree-aware path: git's toplevel resolves to the worktree dir, not the
# main repo. Falls back to $PWD outside a git checkout.
agent_cwd() {
  git rev-parse --show-toplevel 2>/dev/null || echo "$PWD"
}

# Current branch (empty string on detached HEAD or non-git dir).
agent_branch() {
  git symbolic-ref --short -q HEAD 2>/dev/null || echo ""
}

# Delete channel directories whose log hasn't been touched in
# $AGENT_CHAT_TTL_DAYS days. Silent; failures are non-fatal so a stale
# permissions issue can't break send/join.
sweep_old_channels() {
  [[ -d "$AGENT_CHAT_ROOT" ]] || return 0
  find "$AGENT_CHAT_ROOT" -mindepth 2 -maxdepth 2 -name log \
    -mtime "+${AGENT_CHAT_TTL_DAYS}" -print 2>/dev/null \
    | while IFS= read -r old_log; do
        rm -rf "$(dirname "$old_log")" 2>/dev/null || true
      done
}

sweep_old_channels
