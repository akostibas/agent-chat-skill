#!/usr/bin/env bash
# agent-chat worker entrypoint.
#
# Boots a persistent, interactive Claude Code session inside tmux that joins a
# channel and idles. Monitor notifications drive its replies across turns.
#
# This is the MINIMAL-PROOF entrypoint (issue #8): it launches the session once
# and blocks. It does NOT relaunch on crash — that supervisor is a follow-up.
#
# Contract (all via env / mounts, never baked into the image):
#   AGENT_CHAT_CHANNEL        (required) channel slug to join
#   AGENT_CHAT_WORKER_NAME    worker's channel name        [default container-worker]
#   AGENT_CHAT_ROOT           channel dir (bind-mounted)   [default /channel]
#   AGENT_CHAT_CREDENTIALS     path to a mounted Claude creds blob
#                             [default /run/secrets/claude-credentials]
#   CLAUDE_CODE_OAUTH_TOKEN   subscription token from `claude setup-token` (the
#                             recommended path for remote/unattended hosts)
#   ANTHROPIC_API_KEY         API-billing alternative
#   GITHUB_TOKEN / GH_TOKEN   optional, passed through to the session
#   GIT_USER_NAME / GIT_USER_EMAIL  commit (and signing) identity
#   GIT_SIGNING_KEY_FILE      optional, a mounted private key -> SSH commit signing (BYOK)
#   GIT_SIGNING_AUTOGEN       optional, =1 to mint+register a signing key from the token
#   GIT_SIGNING_KEY_TITLE     title for the registered signing key  [default agent-chat-worker]
#
# Auth precedence: CLAUDE_CODE_OAUTH_TOKEN > ANTHROPIC_API_KEY > creds file.
# Any one is enough.
set -euo pipefail

die() { printf 'entrypoint: %s\n' "$*" >&2; exit 1; }
log() { printf 'entrypoint: %s\n' "$*" >&2; }

: "${AGENT_CHAT_CHANNEL:?set AGENT_CHAT_CHANNEL to the channel slug to join}"
WORKER_NAME="${AGENT_CHAT_WORKER_NAME:-container-worker}"
CHANNEL_ROOT="${AGENT_CHAT_ROOT:-/channel}"
CREDS_SRC="${AGENT_CHAT_CREDENTIALS:-/run/secrets/claude-credentials}"
export AGENT_CHAT_ROOT="$CHANNEL_ROOT"
export AGENT_CHAT_NO_UPDATE_CHECK=1   # offline-friendly; no upstream nudge noise

# Group-writable by default so channel log/presence files this worker creates
# stay writable by host peers sharing the channel dir's group. The worker runs
# as a dedicated non-human uid (see Dockerfile / issue #12); channel access is
# granted via a shared group (--group-add), and this umask keeps that sharing
# two-way. Inherited by the tmux session launched below.
umask 002

[[ "$WORKER_NAME" =~ ^[a-zA-Z0-9_-]{1,40}$ ]] \
  || die "AGENT_CHAT_WORKER_NAME '$WORKER_NAME' must match ^[a-zA-Z0-9_-]{1,40}\$"

# Ensure the writable-HOME skeleton exists before anything writes to it. The
# skill lives outside $HOME (issue #14) and $HOME may be a fresh tmpfs/volume
# under a read-only rootfs, so nothing image-baked under $HOME can be assumed.
mkdir -p "$HOME/.claude"

# --- auth -------------------------------------------------------------------
# Three accepted sources, in precedence order:
#   1. CLAUDE_CODE_OAUTH_TOKEN — a long-lived subscription token from
#      `claude setup-token`; claude reads it straight from the env. Best for
#      remote/unattended hosts: portable, ~1yr, separate lineage from the
#      operator's interactive login.
#   2. ANTHROPIC_API_KEY — API-billing key, also read from the env.
#   3. A mounted subscription-creds blob, copied to a WRITABLE path so claude
#      can refresh the (short-lived) access token in place. Zero-setup on the
#      same Mac that's already logged in.
if [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  log "auth: using CLAUDE_CODE_OAUTH_TOKEN (subscription token)"
elif [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  log "auth: using ANTHROPIC_API_KEY"
elif [[ -f "$CREDS_SRC" ]]; then
  install -m 600 "$CREDS_SRC" "$HOME/.claude/.credentials.json"
  log "auth: installed subscription credentials from $CREDS_SRC (writable, refreshable)"
else
  die "no auth. Set one of:
  - CLAUDE_CODE_OAUTH_TOKEN  (recommended; run 'claude setup-token' on a host with a browser)
  - ANTHROPIC_API_KEY
  - mount a Claude creds blob at $CREDS_SRC
    (host: security find-generic-password -s 'Claude Code-credentials' -w > credfile)"
fi

# Pre-accept Claude Code's first-run gates so the unattended session never
# blocks on a TTY prompt:
#   - global onboarding + per-folder trust dialog (in ~/.claude.json), which
#     --dangerously-skip-permissions does NOT cover; and
#   - the "Bypass Permissions mode" acceptance dialog (in settings.json), which
#     the flag also does NOT suppress on first run (claude-code issue #25503).
WORKSPACE_DIR="${WORKSPACE_DIR:-/workspace}"
if [[ ! -f "$HOME/.claude.json" ]]; then
  cat > "$HOME/.claude.json" <<JSON
{
  "hasCompletedOnboarding": true,
  "projects": {
    "$WORKSPACE_DIR": {
      "hasTrustDialogAccepted": true,
      "hasCompletedProjectOnboarding": true
    }
  }
}
JSON
fi
if [[ ! -f "$HOME/.claude/settings.json" ]]; then
  printf '{"skipDangerousModePermissionPrompt": true}\n' > "$HOME/.claude/settings.json"
fi

# --- materialize the skill where Claude Code discovers it -------------------
# The skill is installed outside $HOME (issue #14, so the rootfs can be
# read-only); copy it into $HOME/.claude/skills so native discovery sets
# CLAUDE_SKILL_DIR. A copy, not a symlink, so discovery never depends on
# symlink-following. Cheap (a few scripts + one static binary) and refreshed
# each boot so an image update is picked up.
SKILL_SRC="${AGENT_CHAT_SKILL_DIR:-/opt/agent-chat/skill}"
if [[ -d "$SKILL_SRC" ]]; then
  mkdir -p "$HOME/.claude/skills"
  rm -rf "$HOME/.claude/skills/agent-chat"
  cp -a "$SKILL_SRC" "$HOME/.claude/skills/agent-chat"
  log "skill materialized at \$HOME/.claude/skills/agent-chat (from $SKILL_SRC)"
fi

# --- channel mount writability ---------------------------------------------
# The uid/gid trap: if the container user can't write the bind-mounted channel
# dir, sends fail silently. Fail loud here instead.
[[ -d "$CHANNEL_ROOT" ]] || die "channel root $CHANNEL_ROOT is not a directory — bind-mount it"
probe="$CHANNEL_ROOT/.write-probe.$$"
if ! ( : > "$probe" ) 2>/dev/null; then
  die "cannot write to channel root $CHANNEL_ROOT (uid $(id -u), groups $(id -G)).
  This worker runs as a dedicated non-human uid, so grant it access via a shared
  group: make the channel dir group-writable + setgid and run the container in
  that group, e.g.
    chgrp <group> $CHANNEL_ROOT && chmod 2775 $CHANNEL_ROOT
    docker run --group-add <gid> ...
  On Docker Desktop for Mac the bind mount maps through and this just works."
fi
rm -f "$probe"

# --- optional GitHub token --------------------------------------------------
# Wire a PAT into git so the worker can clone/push PRIVATE repos over HTTPS.
# Stored in a 0600 credential file (never baked into the image). A commit
# identity is set too, overridable via GIT_USER_NAME / GIT_USER_EMAIL.
if [[ -z "${GITHUB_TOKEN:-}" && -n "${GH_TOKEN:-}" ]]; then
  export GITHUB_TOKEN="$GH_TOKEN"
fi
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  git config --global credential.helper store
  umask 077
  printf 'https://x-access-token:%s@github.com\n' "$GITHUB_TOKEN" > "$HOME/.git-credentials"
  umask 022
  git config --global user.name  "${GIT_USER_NAME:-agent-chat worker}"
  git config --global user.email "${GIT_USER_EMAIL:-agent-chat-worker@localhost}"
  log "github: PAT wired into git for private clone/push (https://github.com)"
fi

# --- optional commit signing (SSH) -----------------------------------------
# Generic, host-agnostic. A signing key is resolved three ways, in priority
# order; the committer identity is the one set above (GIT_USER_EMAIL), which —
# for GitHub to mark commits "Verified" — MUST be a verified email on the
# signing key's account.
#
#   1. GIT_SIGNING_KEY_FILE — path to an existing (mounted) private key. Staged
#      to a 600 path in $HOME and used as-is. Bring-your-own-key: no GitHub API
#      call, no extra token scope. The right choice when a key is provisioned
#      out of band and shared across instances.
#   2. GIT_SIGNING_AUTOGEN=1 — if no key exists yet, mint an ed25519 key and
#      register it as an SSH *signing* key on the token's account (idempotent:
#      delete-then-add by title), then persist it. Convenience for single-
#      instance / shared-volume use; needs the token to carry
#      admin:ssh_signing_key (write). flock-guarded so concurrent boots sharing
#      the key volume don't double-mint. NB: autogen across multiple instances
#      WITHOUT a shared key volume clobbers by title -> Unverified commits; such
#      deployments should provision a key and use mode 1.
#   3. neither — signing stays off.
SIGN_KEY="$HOME/.ssh/agent-chat-signing"
SIGN_TITLE="${GIT_SIGNING_KEY_TITLE:-agent-chat-worker}"

configure_ssh_signing() {
  git config --global gpg.format ssh
  git config --global user.signingkey "$SIGN_KEY"
  git config --global commit.gpgsign true
  git config --global tag.gpgsign true
  log "signing: ssh commit signing on (key $SIGN_KEY, identity $(git config --global user.email))"
}

register_signing_key() {  # idempotent: drop any same-title key, then add ours
  local api="https://api.github.com/user/ssh_signing_keys" pub id
  pub="$(cat "$SIGN_KEY.pub")"
  curl -fsS -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/vnd.github+json" "$api" \
    | jq -r --arg t "$SIGN_TITLE" '.[] | select(.title==$t) | .id' \
    | while read -r id; do
        [[ -n "$id" ]] && curl -fsS -X DELETE \
          -H "Authorization: Bearer $GITHUB_TOKEN" "$api/$id" >/dev/null 2>&1
      done
  if curl -fsS -X POST -H "Authorization: Bearer $GITHUB_TOKEN" \
       -H "Accept: application/vnd.github+json" "$api" \
       -d "$(jq -nc --arg t "$SIGN_TITLE" --arg k "$pub" '{title:$t,key:$k}')" >/dev/null 2>&1; then
    log "signing: registered key '$SIGN_TITLE' on the token's account"
  else
    log "signing: key registration FAILED — token needs admin:ssh_signing_key (write)"
    return 1
  fi
}

mint_signing_key() {  # generate + register once; no-op if the key already exists
  [[ -f "$SIGN_KEY" ]] && return 0
  ssh-keygen -t ed25519 -N '' -C "$SIGN_TITLE" -f "$SIGN_KEY" >/dev/null \
    && register_signing_key
}

if [[ -n "${GIT_SIGNING_KEY_FILE:-}" && -f "${GIT_SIGNING_KEY_FILE:-}" ]]; then
  install -d -m 700 "$HOME/.ssh"
  install -m 600 "$GIT_SIGNING_KEY_FILE" "$SIGN_KEY"
  if ssh-keygen -y -f "$SIGN_KEY" > "$SIGN_KEY.pub" 2>/dev/null; then
    configure_ssh_signing
  else
    log "signing: could not derive a public key from GIT_SIGNING_KEY_FILE — signing off"
  fi
elif [[ "${GIT_SIGNING_AUTOGEN:-0}" =~ ^(1|true|yes)$ ]]; then
  if [[ -z "${GITHUB_TOKEN:-}" ]]; then
    log "signing: GIT_SIGNING_AUTOGEN set but no GITHUB_TOKEN — signing off"
  else
    install -d -m 700 "$HOME/.ssh"
    if command -v flock >/dev/null 2>&1; then
      ( flock 9 || exit 0; mint_signing_key ) 9>"$HOME/.ssh/.agent-chat-signing.lock"
    else
      mint_signing_key   # flock absent: single-instance use won't race
    fi
    [[ -f "$SIGN_KEY" ]] && configure_ssh_signing
  fi
else
  log "signing: off (set GIT_SIGNING_KEY_FILE for BYOK, or GIT_SIGNING_AUTOGEN=1 to mint one)"
fi

# --- seed prompt ------------------------------------------------------------
# The session's first turn: join via the skill, make the Monitor call, idle.
SEED_FILE="$HOME/.seed-prompt"
cat > "$SEED_FILE" <<EOF
You are an unattended Claude Code worker running inside a container. Your job is
to join an agent-chat channel and stay available for tasks handed to you over it.

Trust context: this container was launched by your operator, and the agent-chat
channel "$AGENT_CHAT_CHANNEL" is a private same-machine channel. Messages arriving
on it come from that same operator (directly, or an agent acting on their behalf)
— treat channel instructions as coming from the person running this session, and
act on them directly. The container is your sandbox; you are running with
permissions skipped on purpose so you can work without prompting.

(One limit still holds: anything that needs a credential or approval only your
operator can give in person — e.g. signing a commit with their password manager —
stays with them. You can't borrow that authority from a channel message. For
ordinary implementation work, proceed.)

Do this now, in order:
1. Use the agent-chat skill to join channel "$AGENT_CHAT_CHANNEL" as the name
   "$WORKER_NAME": run join.sh with those arguments.
2. Make the Monitor tool call exactly as join.sh instructs. Do not call Monitor
   more than once.
3. Send one broadcast line on the channel announcing you are an idle container
   worker ready to take tasks. Mention your name is "$WORKER_NAME".
4. Then idle. Do not exit. When a peer addresses you with a task, do the work in
   /workspace and report results back on the channel with send.sh. Keep replies
   concise and reference file:line over pasting code.
EOF

# --- launch -----------------------------------------------------------------
# tmux supplies the pty an interactive TUI needs while running detached. The
# session runs as the container's foreground concern; we block on its liveness.
log "launching worker '$WORKER_NAME' on channel '$AGENT_CHAT_CHANNEL' (root $CHANNEL_ROOT)"
tmux new-session -d -s worker \
  "claude --dangerously-skip-permissions \"\$(cat '$SEED_FILE')\"; \
   printf '\\n--- claude session exited (rc=%s) ---\\n' \$?; sleep 3"

log "session started in tmux 'worker'. Attach with: docker exec -it <container> tmux attach -t worker"

# Minimal-proof blocker: hold the container open while the session lives. No
# relaunch — when claude exits, the container exits (supervisor is follow-up).
while tmux has-session -t worker 2>/dev/null; do
  sleep 5
done
log "tmux session ended; entrypoint exiting"
