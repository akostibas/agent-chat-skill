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
#   --name NAME        worker's channel name        (default: container-worker)
#   --root DIR         host channel dir to mount     (default: ~/.claude/agent-chat)
#   --workspace DIR    host repo to mount at /workspace (default: none)
#   --image NAME       image to run                 (default: agent-chat-worker)
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

NAME="container-worker"
ROOT="$HOME/.claude/agent-chat"
WORKSPACE=""
IMAGE="agent-chat-worker"
USE_API_KEY=0
DETACH="-d"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)       NAME="$2"; shift 2 ;;
    --root)       ROOT="$2"; shift 2 ;;
    --workspace)  WORKSPACE="$2"; shift 2 ;;
    --image)      IMAGE="$2"; shift 2 ;;
    --api-key)    USE_API_KEY=1; shift ;;
    --foreground) DETACH=""; shift ;;
    *) die "unknown option: $1" ;;
  esac
done

[[ "$SLUG" =~ ^[a-zA-Z0-9_-]{1,40}$ ]] || die "invalid channel slug '$SLUG'"
docker image inspect "$IMAGE" >/dev/null 2>&1 \
  || die "image '$IMAGE' not found — run: make docker-build"

mkdir -p "$ROOT" || die "cannot create channel root $ROOT"
CONTAINER="agent-chat-worker-$SLUG"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true

# --- assemble run args -------------------------------------------------------
run_args=( --name "$CONTAINER" --rm
           -e "AGENT_CHAT_CHANNEL=$SLUG"
           -e "AGENT_CHAT_WORKER_NAME=$NAME"
           -v "$ROOT:/channel" )

[[ -n "$WORKSPACE" ]] && run_args+=( -v "$WORKSPACE:/workspace" )
[[ -n "${GITHUB_TOKEN:-}" ]] && run_args+=( -e "GITHUB_TOKEN=$GITHUB_TOKEN" )

CREDS_TMP=""
cleanup() { [[ -n "$CREDS_TMP" && -f "$CREDS_TMP" ]] && { shred -u "$CREDS_TMP" 2>/dev/null || rm -f "$CREDS_TMP"; }; }
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
echo "docker-worker: starting '$NAME' on channel '$SLUG' (root $ROOT)"
docker run $DETACH "${run_args[@]}" "$IMAGE"

if [[ -n "$DETACH" ]]; then
  # Give the entrypoint a moment to copy creds in before we shred the host copy.
  sleep 4
  echo "docker-worker: container '$CONTAINER' is up."
  echo "  attach:  docker exec -it $CONTAINER tmux attach -t worker"
  echo "  logs:    docker logs -f $CONTAINER"
  echo "  stop:    docker rm -f $CONTAINER"
fi
