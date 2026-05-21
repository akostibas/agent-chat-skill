#!/bin/bash
# Shared helpers for agent-mail scripts. Source, don't execute.

set -euo pipefail

AGENT_MAIL_ROOT="${AGENT_MAIL_ROOT:-$HOME/.claude/agent-mail}"
IDENT_RE='^[a-zA-Z0-9_-]{1,40}$'

die() { echo "agent-mail: $*" >&2; exit 1; }

validate_ident() {
  local kind="$1" value="$2"
  [[ "$value" =~ $IDENT_RE ]] || die "invalid $kind '$value' (must match $IDENT_RE)"
}

channel_dir() { echo "$AGENT_MAIL_ROOT/$1"; }
channel_log() { echo "$AGENT_MAIL_ROOT/$1/log"; }
channel_lock() { echo "$AGENT_MAIL_ROOT/$1/log.lock"; }

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
