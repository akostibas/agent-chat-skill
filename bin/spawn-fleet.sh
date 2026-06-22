#!/usr/bin/env bash
# Spawn a fleet of N containerized agent-chat workers for a coordinator to drive
# unattended, then print how to join and tear them down. See issue #17 and
# docs/adr/0007-coordinator-spawned-worker-fleet.md.
#
# Each worker is a hardened container (ADR-0006) that:
#   - joins a PRIVATE, ephemeral channel (its own temp root, NOT your global
#     ~/.claude/agent-chat) under an auto-generated unique name,
#   - clones the target repo fresh into /workspace at boot, and
#   - runs --dangerously-skip-permissions with the interactive tools disabled, so
#     it never stalls waiting for a human (the original #17 failure).
# The coordinator (you, the launching session) joins the SAME channel, dispatches
# a task + branch to each worker, and merges the branches they push back.
#
# Usage:
#   bin/spawn-fleet.sh -n N [options]
#
# Options:
#   -n, --count N    number of workers to spawn        (default 3; 1..20)
#   --repo URL       repo workers clone     (default: origin remote of the cwd)
#   --slug SLUG      channel slug                       (default: fleet-<id>)
#   --root DIR       host channel dir to mount   (default: $TMPDIR/agent-chat-fleet-<id>)
#   --id ID          fleet id used for labels/teardown  (default: random)
#   --image NAME     worker image                       (default: agent-chat-worker)
#   --api-key        bill workers to $ANTHROPIC_API_KEY instead of subscription creds
#
# Auth is inherited by each worker from this environment, same precedence as
# docker-worker.sh: $CLAUDE_CODE_OAUTH_TOKEN > --api-key ($ANTHROPIC_API_KEY) >
# macOS Keychain. A fleet really wants CLAUDE_CODE_OAUTH_TOKEN (one env for all N).
# $GITHUB_TOKEN, if set, is passed through so workers can clone/push private repos.
set -euo pipefail

die()  { printf 'spawn-fleet: %s\n' "$*" >&2; exit 1; }
note() { printf 'spawn-fleet: %s\n' "$*" >&2; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

COUNT=3
REPO=""
SLUG=""
ROOT=""
FLEET_ID=""
IMAGE="agent-chat-worker"
API_KEY_FLAG=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -n|--count) COUNT="$2"; shift 2 ;;
    --repo)     REPO="$2"; shift 2 ;;
    --slug)     SLUG="$2"; shift 2 ;;
    --root)     ROOT="$2"; shift 2 ;;
    --id)       FLEET_ID="$2"; shift 2 ;;
    --image)    IMAGE="$2"; shift 2 ;;
    --api-key)  API_KEY_FLAG="--api-key"; shift ;;
    -h|--help)  sed -n '2,30p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

# --- preconditions ----------------------------------------------------------
command -v docker >/dev/null 2>&1 || die "docker not found on PATH"
[[ "$COUNT" =~ ^[0-9]+$ ]] || die "--count must be a positive integer (got '$COUNT')"
(( COUNT >= 1 )) || die "--count must be >= 1"
(( COUNT <= 20 )) || die "--count $COUNT exceeds the safety cap of 20 — raise it deliberately if you mean it"
(( COUNT <= 8 )) || note "WARNING: spawning $COUNT workers; each is a full Claude Code session (token + CPU cost)."

docker image inspect "$IMAGE" >/dev/null 2>&1 \
  || die "image '$IMAGE' not found — build it first: make docker-build"

# Auth: a fleet wants one env shared across all workers. Without a token or
# --api-key we fall back to per-container Keychain extraction (macOS only).
if [[ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" && -z "$API_KEY_FLAG" ]]; then
  [[ "$(uname)" == "Darwin" ]] \
    || die "no auth: set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or pass --api-key"
  note "no CLAUDE_CODE_OAUTH_TOKEN/--api-key — falling back to per-container Keychain (slower for a fleet)."
fi
[[ -n "$API_KEY_FLAG" && -z "${ANTHROPIC_API_KEY:-}" ]] && die "--api-key given but ANTHROPIC_API_KEY is empty"

# --- repo to clone ----------------------------------------------------------
# Workers clone over HTTPS with the injected token, so normalize SSH remotes.
normalize_repo() {
  local url="$1"
  case "$url" in
    git@github.com:*)        echo "https://github.com/${url#git@github.com:}" ;;
    ssh://git@github.com/*)  echo "https://github.com/${url#ssh://git@github.com/}" ;;
    *)                       echo "$url" ;;
  esac
}
if [[ -z "$REPO" ]]; then
  REPO="$(git remote get-url origin 2>/dev/null)" \
    || die "no --repo given and the cwd has no 'origin' remote — pass --repo URL"
fi
REPO="$(normalize_repo "$REPO")"
case "$REPO" in
  https://*|http://*|git@*|ssh://*) : ;;
  *) die "repo '$REPO' doesn't look like a clonable URL" ;;
esac

# --- fleet identity ---------------------------------------------------------
if [[ -z "$FLEET_ID" ]]; then
  FLEET_ID="$(od -An -tx1 -N4 /dev/urandom | tr -d ' \n')"
fi
[[ "$FLEET_ID" =~ ^[a-zA-Z0-9_-]{1,32}$ ]] || die "invalid --id '$FLEET_ID'"
SLUG="${SLUG:-fleet-$FLEET_ID}"
[[ "$SLUG" =~ ^[a-zA-Z0-9_-]{1,40}$ ]] || die "invalid --slug '$SLUG'"
ROOT="${ROOT:-${TMPDIR:-/tmp}/agent-chat-fleet-$FLEET_ID}"
ROOT="${ROOT%/}"

mkdir -p "$ROOT" || die "cannot create channel root $ROOT"

# --- launch -----------------------------------------------------------------
note "fleet $FLEET_ID: launching $COUNT worker(s) on channel '$SLUG'"
note "  repo:    $REPO"
note "  channel: $ROOT (ephemeral; removed at teardown)"

launched=()
failed=0
for (( i = 1; i <= COUNT; i++ )); do
  container="agent-chat-fleet-$FLEET_ID-w$i"
  note "[$i/$COUNT] starting $container"
  if "$SCRIPT_DIR/docker-worker.sh" "$SLUG" \
        --root "$ROOT" \
        --image "$IMAGE" \
        --clone "$REPO" \
        --container "$container" \
        --label "agent-chat-fleet=$FLEET_ID" \
        ${API_KEY_FLAG:+$API_KEY_FLAG}; then
    launched+=( "$container" )
  else
    note "[$i/$COUNT] FAILED to start $container"
    failed=$((failed + 1))
  fi
done

(( ${#launched[@]} > 0 )) || die "no workers started; see errors above. Channel root left at $ROOT"

# --- briefing for the coordinator -------------------------------------------
# join.sh of the INSTALLED skill (the coordinator session uses that, not this
# repo). Fall back to the repo's copy if the skill isn't installed.
join_sh="$HOME/.claude/skills/agent-chat/join.sh"
[[ -x "$join_sh" ]] || join_sh="$SCRIPT_DIR/../skill/join.sh"

cat >&2 <<EOF

spawn-fleet: fleet '$FLEET_ID' is up — ${#launched[@]}/$COUNT worker(s) launched$( ((failed>0)) && printf ', %s failed' "$failed" ).

  Join the channel as coordinator (same ephemeral root the workers use):
    AGENT_CHAT_ROOT="$ROOT" "$join_sh" "$SLUG" --as coordinator
  then make the Monitor call it prints. Workers announce themselves as they come
  up; dispatch each a task by @name and tell it which branch to push.

  Watch a worker:   docker logs -f ${launched[0]}
  Attach a worker:  docker exec -it ${launched[0]} tmux attach -t worker
  List the fleet:   docker ps --filter label=agent-chat-fleet=$FLEET_ID
  Tear it all down: "$SCRIPT_DIR/teardown-fleet.sh" "$FLEET_ID"
EOF
