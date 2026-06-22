#!/usr/bin/env bash
# Launch a containerized agent-chat worker, pointed at a host channel dir.
#
# Handles the host side of the secrets contract: it extracts the operator's
# Claude Code subscription credentials (macOS Keychain) into a short-lived 0600
# file, mounts it read-only into the container (where the entrypoint copies it
# to a writable path for token refresh), then shreds the host copy once the
# container is up. The token never lands in the image or in a long-lived file.
#
# Usage:
#   bin/docker-worker.sh <channel-slug> [options]
#
# Options:
#   --name NAME        worker's channel name   (default: the channel auto-names it)
#   --root DIR         host channel dir to mount     (default: ~/.claude/agent-chat)
#   --workspace DIR    host repo to mount at /workspace (default: none)
#   --image NAME       image to run                 (default: agent-chat-worker)
#   --container NAME   docker container name (default: agent-chat-worker-<slug>[-<name>]).
#                      Override to run several auto-named workers on one channel
#                      without container-name collisions (see bin/spawn-fleet.sh).
#   --clone URL        git URL the worker clones into /workspace at boot
#                      (AGENT_CHAT_CLONE_REPO; pushes its branch back to merge)
#   --label K=V        docker label to attach (repeatable; used for fleet teardown)
#   --disallow "T..."  override the worker's blocked tools (AGENT_CHAT_DISALLOWED_TOOLS);
#                      pass "" to allow all. Default (unset) blocks the interactive
#                      tools that would hang an unattended worker.
#   --group-add GID    supplementary gid for the channel dir (native Linux: the
#                      worker runs as a non-human uid, so it needs the channel
#                      dir's group to write it)
#   --api-key          use $ANTHROPIC_API_KEY instead of subscription creds
#   --foreground       run attached (don't detach)
#
# Auth precedence: $CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token`, the
# portable path for non-Mac/remote hosts) > --api-key ($ANTHROPIC_API_KEY) >
# macOS Keychain subscription creds.
set -euo pipefail

die() { printf 'docker-worker: %s\n' "$*" >&2; exit 1; }

[[ $# -ge 1 ]] || die "usage: bin/docker-worker.sh <channel-slug> [options]"
SLUG="$1"; shift

NAME=""   # empty => the container's join auto-generates a unique name
ROOT="$HOME/.claude/agent-chat"
WORKSPACE=""
IMAGE="agent-chat-worker"
CONTAINER_OVERRIDE=""
CLONE=""
DISALLOW=""
DISALLOW_SET=0   # distinguishes unset (entrypoint default) from --disallow "" (opt out)
LABELS=()
USE_API_KEY=0
DETACH="-d"
GROUP_ADD=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)       NAME="$2"; shift 2 ;;
    --root)       ROOT="$2"; shift 2 ;;
    --workspace)  WORKSPACE="$2"; shift 2 ;;
    --image)      IMAGE="$2"; shift 2 ;;
    --container)  CONTAINER_OVERRIDE="$2"; shift 2 ;;
    --clone)      CLONE="$2"; shift 2 ;;
    --label)      LABELS+=( "$2" ); shift 2 ;;
    --disallow)   DISALLOW="$2"; DISALLOW_SET=1; shift 2 ;;
    --group-add)  GROUP_ADD="$2"; shift 2 ;;   # gid for the shared channel dir (native Linux)
    --api-key)    USE_API_KEY=1; shift ;;
    --foreground) DETACH=""; shift ;;
    *) die "unknown option: $1" ;;
  esac
done

[[ "$SLUG" =~ ^[a-zA-Z0-9_-]{1,40}$ ]] || die "invalid channel slug '$SLUG'"
docker image inspect "$IMAGE" >/dev/null 2>&1 \
  || die "image '$IMAGE' not found — run: make docker-build"

[[ -z "$NAME" || "$NAME" =~ ^[a-zA-Z0-9_-]{1,40}$ ]] || die "invalid --name '$NAME'"
[[ -z "$CONTAINER_OVERRIDE" || "$CONTAINER_OVERRIDE" =~ ^[a-zA-Z0-9_.-]{1,128}$ ]] \
  || die "invalid --container '$CONTAINER_OVERRIDE'"
if [[ ${#LABELS[@]} -gt 0 ]]; then
  for l in "${LABELS[@]}"; do
    [[ "$l" == *=* ]] || die "invalid --label '$l' (expected KEY=VALUE)"
  done
fi
mkdir -p "$ROOT" || die "cannot create channel root $ROOT"
# Container name: --container wins (lets a coordinator give each fleet worker a
# unique container name while channels still auto-name). Else --name suffixes the
# slug so several named workers coexist; bare slug otherwise (one convenience
# worker per channel). For a fleet, bin/spawn-fleet.sh sets --container per worker.
CONTAINER="${CONTAINER_OVERRIDE:-agent-chat-worker-$SLUG${NAME:+-$NAME}}"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true

# --- assemble run args -------------------------------------------------------
run_args=( --name "$CONTAINER" --rm
           -e "AGENT_CHAT_CHANNEL=$SLUG"
           -v "$ROOT:/channel" )
# Only pin a name when explicitly given; otherwise let the container auto-name.
[[ -n "$NAME" ]] && run_args+=( -e "AGENT_CHAT_WORKER_NAME=$NAME" )

[[ -n "$WORKSPACE" ]] && run_args+=( -v "$WORKSPACE:/workspace" )
[[ -n "$CLONE" ]] && run_args+=( -e "AGENT_CHAT_CLONE_REPO=$CLONE" )
[[ "$DISALLOW_SET" == 1 ]] && run_args+=( -e "AGENT_CHAT_DISALLOWED_TOOLS=$DISALLOW" )
[[ -n "$GROUP_ADD" ]] && run_args+=( --group-add "$GROUP_ADD" )
[[ -n "${GITHUB_TOKEN:-}" ]] && run_args+=( -e "GITHUB_TOKEN=$GITHUB_TOKEN" )
if [[ ${#LABELS[@]} -gt 0 ]]; then
  for l in "${LABELS[@]}"; do run_args+=( --label "$l" ); done
fi

CREDS_TMP=""
# NB: keep this an `if` (not a `&&` chain). As the EXIT trap it sets the script's
# exit status, and a `&&` whose left side is false returns 1 — which made the
# launcher exit 1 on the token/--api-key path (where CREDS_TMP is empty) even
# though the worker started fine.
cleanup() {
  if [[ -n "$CREDS_TMP" && -f "$CREDS_TMP" ]]; then
    shred -u "$CREDS_TMP" 2>/dev/null || rm -f "$CREDS_TMP"
  fi
}
trap cleanup EXIT

if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  run_args+=( -e "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN" )
  echo "docker-worker: auth via CLAUDE_CODE_OAUTH_TOKEN (subscription token)"
elif [[ "$USE_API_KEY" == 1 ]]; then
  [[ -n "${ANTHROPIC_API_KEY:-}" ]] || die "--api-key given but ANTHROPIC_API_KEY is empty"
  run_args+=( -e "ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY" )
  echo "docker-worker: auth via ANTHROPIC_API_KEY"
else
  [[ "$(uname)" == "Darwin" ]] || die "subscription-creds extraction is macOS-only; use --api-key elsewhere"
  CREDS_TMP="$(mktemp -t agent-chat-creds.XXXXXX)"
  chmod 600 "$CREDS_TMP"
  security find-generic-password -s "Claude Code-credentials" -w > "$CREDS_TMP" 2>/dev/null \
    || die "could not read 'Claude Code-credentials' from Keychain — are you logged into Claude Code?"
  [[ -s "$CREDS_TMP" ]] || die "extracted credentials are empty"
  run_args+=( -v "$CREDS_TMP:/run/secrets/claude-credentials:ro" )
  echo "docker-worker: auth via subscription credentials (Keychain)"
fi

# --- launch ------------------------------------------------------------------
echo "docker-worker: starting '${NAME:-<auto-named>}' on channel '$SLUG' (root $ROOT)"
docker run $DETACH "${run_args[@]}" "$IMAGE"

if [[ -n "$DETACH" ]]; then
  # Give the entrypoint a moment to copy creds in before we shred the host copy.
  # Configurable (declared, not buried) so a fleet spawn or a test can shorten it.
  sleep "${AGENT_CHAT_LAUNCH_SETTLE_SECS:-4}"
  echo "docker-worker: container '$CONTAINER' is up."
  echo "  attach:  docker exec -it $CONTAINER tmux attach -t worker"
  echo "  logs:    docker logs -f $CONTAINER"
  echo "  stop:    docker rm -f $CONTAINER"
fi
