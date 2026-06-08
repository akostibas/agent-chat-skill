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

# Names of agents currently present in a channel, one per line (empty when the
# channel has no live members). This is the roster send.sh resolves @mentions
# against: an @token that names no present member isn't an address, so the
# message broadcasts instead of being narrowed to a phantom peer.
channel_members() {
  local slug="$1" pdir f
  pdir="$(presence_dir "$slug")"
  [[ -d "$pdir" ]] || return 0
  for f in "$pdir"/*; do
    [[ -e "$f" ]] || continue   # empty glob
    basename "$f"
  done
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

# Throttled, best-effort upstream version check. Reads the installed VERSION
# (written by `make install`), and at most once per AGENT_CHAT_UPDATE_TTL_SECS
# compares it against the latest GitHub release tag, printing a one-line stderr
# nudge when behind. The nudge goes to stderr so it reaches the agent without
# polluting the stdout stream Monitor parses. Network and parse failures are
# swallowed — this must never break send/join/stream. Opt out entirely with
# AGENT_CHAT_NO_UPDATE_CHECK=1. The stamp file in $TMPDIR throttles globally
# (one check per machine per TTL, regardless of channel).
AGENT_CHAT_REPO="${AGENT_CHAT_REPO:-akostibas/agent-chat-skill}"
check_for_update() {
  [[ -n "${AGENT_CHAT_NO_UPDATE_CHECK:-}" ]] && return 0
  local skill_dir version_file current stamp ttl now mt json latest
  skill_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  version_file="$skill_dir/VERSION"
  [[ -f "$version_file" ]] || return 0
  current="$(cat "$version_file" 2>/dev/null || true)"
  [[ -n "$current" ]] || return 0
  ttl="${AGENT_CHAT_UPDATE_TTL_SECS:-86400}"
  stamp="${TMPDIR:-/tmp}/agent-chat-update-check"
  now="$(date +%s)"
  mt="$(file_mtime "$stamp")"
  [[ -n "$mt" ]] && (( now - mt < ttl )) && return 0
  touch "$stamp" 2>/dev/null || true   # record the attempt now, even if it fails
  command -v curl >/dev/null 2>&1 || return 0
  json="$(curl -fsSL --max-time 2 -H "Accept: application/vnd.github+json" \
    "https://api.github.com/repos/${AGENT_CHAT_REPO}/releases/latest" 2>/dev/null)" || return 0
  if command -v jq >/dev/null 2>&1; then
    latest="$(printf '%s' "$json" | jq -r '.tag_name // empty' 2>/dev/null)"
  else
    latest="$(printf '%s' "$json" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')"
  fi
  [[ -n "$latest" ]] || return 0
  if [[ "$current" != "$latest" ]]; then
    echo "agent-chat: a newer release is available ($current → $latest). To upgrade: bash $skill_dir/update.sh" >&2
  fi
}

sweep_old_channels
check_for_update || true
