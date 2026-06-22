#!/usr/bin/env bash
# Tear down a worker fleet spawned by bin/spawn-fleet.sh: stop+remove every
# container carrying the fleet's label and remove its ephemeral channel root.
# See issue #17 / docs/adr/0007-coordinator-spawned-worker-fleet.md.
#
# Containers are matched by the docker label `agent-chat-fleet=<id>`, never by
# name parsing — so it only ever touches the fleet you name and leaves other
# fleets (and unrelated containers) alone.
#
# Usage:
#   bin/teardown-fleet.sh <fleet-id> [--keep-channel] [--root DIR]
#   bin/teardown-fleet.sh --list          # show all live fleets and exit
#
# It does NOT delete branches workers pushed (they may hold unmerged work) —
# it reminds you to check `git branch -r` instead.
set -euo pipefail

die()  { printf 'teardown-fleet: %s\n' "$*" >&2; exit 1; }
note() { printf 'teardown-fleet: %s\n' "$*" >&2; }

command -v docker >/dev/null 2>&1 || die "docker not found on PATH"

# --- --list: enumerate fleets and exit --------------------------------------
if [[ "${1:-}" == "--list" ]]; then
  ids="$(docker ps -a --filter "label=agent-chat-fleet" \
           --format '{{ index .Labels "agent-chat-fleet" }}' 2>/dev/null | sort -u)"
  if [[ -z "$ids" ]]; then
    note "no fleets found."
    exit 0
  fi
  note "live fleets (id — container count):"
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    n="$(docker ps -aq --filter "label=agent-chat-fleet=$id" | grep -c .)"
    printf '  %s — %s container(s)\n' "$id" "$n" >&2
  done <<< "$ids"
  exit 0
fi

FLEET_ID="${1:-}"
[[ -n "$FLEET_ID" ]] || die "usage: bin/teardown-fleet.sh <fleet-id> [--keep-channel] [--root DIR]  (or --list)"
shift

[[ "$FLEET_ID" =~ ^[a-zA-Z0-9_-]{1,32}$ ]] || die "invalid fleet id '$FLEET_ID'"

KEEP_CHANNEL=0
ROOT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep-channel) KEEP_CHANNEL=1; shift ;;
    --root)         ROOT="$2"; shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done
ROOT="${ROOT:-${TMPDIR:-/tmp}/agent-chat-fleet-$FLEET_ID}"
ROOT="${ROOT%/}"

# --- stop + remove containers by label --------------------------------------
# Read into an array the bash-3.2 way (macOS has no `mapfile`).
ids=()
while IFS= read -r cid; do
  [[ -n "$cid" ]] && ids+=( "$cid" )
done < <(docker ps -aq --filter "label=agent-chat-fleet=$FLEET_ID")
if (( ${#ids[@]} == 0 )); then
  note "no containers labeled agent-chat-fleet=$FLEET_ID (already gone?)."
else
  note "removing ${#ids[@]} container(s) for fleet $FLEET_ID..."
  # docker rm -f stops then removes; --rm containers may already be exiting.
  docker rm -f "${ids[@]}" >/dev/null 2>&1 || note "warning: some containers resisted removal — check 'docker ps -a'."
  note "containers removed."
fi

# --- remove the ephemeral channel root --------------------------------------
if (( KEEP_CHANNEL )); then
  note "keeping channel root $ROOT (--keep-channel)."
elif [[ ! -e "$ROOT" ]]; then
  note "channel root $ROOT already gone."
elif [[ "$(basename "$ROOT")" != agent-chat-fleet-* ]]; then
  # Safety: only auto-remove dirs that look like a fleet root. A custom --root
  # that doesn't match is left for the operator to remove deliberately.
  note "channel root $ROOT doesn't look like a fleet dir — NOT removing it. Remove it yourself if intended."
else
  rm -rf "$ROOT" && note "removed channel root $ROOT."
fi

cat >&2 <<EOF

teardown-fleet: fleet '$FLEET_ID' torn down.
  Workers may have pushed branches that aren't merged yet — this did NOT delete
  them. Check and clean up in your repo, e.g.:
    git fetch --prune && git branch -r | grep -i fleet
EOF
