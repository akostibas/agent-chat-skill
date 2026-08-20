#!/usr/bin/env bash
# agent-chat worker entrypoint.
#
# Boots a persistent, interactive Claude Code session inside tmux that joins a
# channel and idles. The delivery hook injects peer messages between tool calls;
# the idle doorbell wakes the session when it has gone quiet.
#
# This is the MINIMAL-PROOF entrypoint (issue #8): it launches the session once
# and blocks. It does NOT relaunch on crash — that supervisor is a follow-up.
#
# Contract (all via env / mounts, never baked into the image):
#   AGENT_CHAT_CHANNEL        (required) channel slug to join
#   AGENT_CHAT_WORKER_NAME    worker's channel name   [default: join auto-generates a unique name]
#   AGENT_CHAT_ROOT           channel dir (bind-mounted)   [default /channel]
#   AGENT_CHAT_CLONE_REPO     optional git URL; cloned into /workspace at boot
#                             when that dir is empty (fleet workers; issue #17)
#   AGENT_CHAT_DISALLOWED_TOOLS  space-separated Claude tools to block in the
#                             worker session [default "AskUserQuestion
#                             ExitPlanMode"; set EMPTY to allow all]. An
#                             unattended container can't answer an interactive
#                             prompt, so these would hang it — blocked by default.
#   AGENT_CHAT_CREDENTIALS     path to a mounted Claude creds blob
#                             [default /run/secrets/claude-credentials]
#   CLAUDE_CODE_OAUTH_TOKEN   subscription token from `claude setup-token` (the
#                             recommended path for remote/unattended hosts)
#   ANTHROPIC_API_KEY         API-billing alternative
#   GITHUB_TOKEN / GH_TOKEN   optional, passed through to the session
#   GIT_USER_NAME / GIT_USER_EMAIL  commit (and signing) identity
#   GIT_SIGNING_KEY_FILE      optional, a mounted private key -> SSH commit signing (BYOK)
#
# Auth precedence: CLAUDE_CODE_OAUTH_TOKEN > ANTHROPIC_API_KEY > creds file.
# Any one is enough.
set -euo pipefail

die() { printf 'entrypoint: %s\n' "$*" >&2; exit 1; }
log() { printf 'entrypoint: %s\n' "$*" >&2; }

: "${AGENT_CHAT_CHANNEL:?set AGENT_CHAT_CHANNEL to the channel slug to join}"
# Optional. Empty => join auto-generates a unique name. A fixed default would
# break multi-worker channels: post-v0.11.0 join rejects an already-active name,
# so two containers sharing one default (the old "container-worker") would fail
# the second join. Auto-naming sidesteps that; the agent reads its assigned name
# from join.sh's output.
WORKER_NAME="${AGENT_CHAT_WORKER_NAME:-}"
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

[[ -z "$WORKER_NAME" || "$WORKER_NAME" =~ ^[a-zA-Z0-9_-]{1,40}$ ]] \
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
  # Register the delivery hook: it is the ONLY thing that renders channel
  # messages into the session, so an unhooked worker joins and then hears
  # nothing. Idempotent, and it merges into the settings.json written above.
  "$HOME/.claude/skills/agent-chat/agent-chat" hook install \
    || die "could not register the agent-chat delivery hook — the worker would receive no messages"
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

# --- optional commit signing (SSH, bring-your-own-key) -----------------------
# GIT_SIGNING_KEY_FILE points at an existing (mounted) private key; it is staged
# to a 600 path in $HOME and used as-is. Unset means signing stays off.
#
# BYOK only, deliberately: minting a key and registering it on the token's
# GitHub account needed admin:ssh_signing_key (write) on the worker's token, and
# clobbered by title across instances that didn't share a key volume — silently
# producing Unverified commits, the exact thing signing is for. Provision a key
# out of band instead.
#
# The committer identity is the one set above (GIT_USER_EMAIL), which — for
# GitHub to mark commits "Verified" — MUST be a verified email on the signing
# key's account.
SIGN_KEY="$HOME/.ssh/agent-chat-signing"

if [[ -n "${GIT_SIGNING_KEY_FILE:-}" && -f "${GIT_SIGNING_KEY_FILE:-}" ]]; then
  install -d -m 700 "$HOME/.ssh"
  install -m 600 "$GIT_SIGNING_KEY_FILE" "$SIGN_KEY"
  if ssh-keygen -y -f "$SIGN_KEY" > "$SIGN_KEY.pub" 2>/dev/null; then
    git config --global gpg.format ssh
    git config --global user.signingkey "$SIGN_KEY"
    git config --global commit.gpgsign true
    git config --global tag.gpgsign true
    log "signing: ssh commit signing on (key $SIGN_KEY, identity $(git config --global user.email))"
  else
    log "signing: could not derive a public key from GIT_SIGNING_KEY_FILE — signing off"
  fi
else
  log "signing: off (mount a private key and set GIT_SIGNING_KEY_FILE to enable)"
fi

# --- optional repo clone ----------------------------------------------------
# Fleet workers (issue #17) are dispatched against a repo cloned fresh at boot:
# the host launcher passes AGENT_CHAT_CLONE_REPO and the worker pushes its branch
# back to the remote for the coordinator to merge. Runs AFTER the GitHub-token
# wiring above so private clones authenticate. Gated + idempotent: skipped when
# unset (single-worker / Shannon manage their own checkout) or when /workspace
# already holds a checkout, so a restart never clobbers in-progress work.
CLONE_REPO="${AGENT_CHAT_CLONE_REPO:-}"
if [[ -n "$CLONE_REPO" ]]; then
  if [[ -n "$(ls -A "$WORKSPACE_DIR" 2>/dev/null)" ]]; then
    log "clone: $WORKSPACE_DIR not empty — keeping existing checkout, skipping clone of $CLONE_REPO"
  else
    log "clone: cloning $CLONE_REPO into $WORKSPACE_DIR"
    git clone "$CLONE_REPO" "$WORKSPACE_DIR" \
      || die "clone failed: $CLONE_REPO (check the URL, and that GITHUB_TOKEN can read it for a private repo)"
  fi
fi

# --- seed prompt ------------------------------------------------------------
# The session's first turn: join via the skill, arm the doorbell, idle.
# The join step is name-aware: with AGENT_CHAT_WORKER_NAME set we pass it as
# --as; without, join auto-generates. Either way the agent reads back its actual
# assigned name and uses that.
if [[ -n "$WORKER_NAME" ]]; then
  JOIN_STEP="join channel \"$AGENT_CHAT_CHANNEL\" as the name \"$WORKER_NAME\": run join.sh with the slug and --as \"$WORKER_NAME\"."
else
  JOIN_STEP="join channel \"$AGENT_CHAT_CHANNEL\": run join.sh with the slug and NO --as flag, so the channel assigns you a unique name."
fi
# When the host launcher cloned a repo for us, tell the worker its checkout is
# ready and how work flows back (push a branch; the coordinator merges/PRs).
if [[ -n "$CLONE_REPO" ]]; then
  WORKSPACE_NOTE="A fresh checkout of $CLONE_REPO is already in /workspace. For a coding task, work on the branch the coordinator names, commit, and \`git push\` that branch to origin — then report the branch name on the channel so the coordinator can merge or open a PR. Do NOT merge to the main branch yourself."
else
  WORKSPACE_NOTE="Do task work in /workspace."
fi
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
1. Use the agent-chat skill to $JOIN_STEP
2. Read the exact name join.sh reports you were assigned. Use THAT name (not a
   guess) for every send and for your announcement below.
3. Arm your idle doorbell exactly as join.sh instructs: run the printed wait.sh
   command with the Bash tool, run_in_background=true. It blocks silently and
   exits when peer traffic arrives. Every time it exits, make any tool call (the
   delivery hook injects the messages) and then re-arm the same command.
4. Send one @all line on the channel announcing you are an idle container
   worker ready to take tasks, and state your assigned name. It MUST be @all:
   addressing sets who you interrupt — @all wakes everyone, @name wakes that
   peer, and a bare unaddressed line is a pull-only FYI that wakes no one, so an
   unaddressed announcement would leave peers unaware you are ready.
5. Then idle. Do not exit. When a peer addresses you with a task, carry it out
   and report results back on the channel with send.sh. $WORKSPACE_NOTE Keep
   replies concise and reference file:line over pasting code.

You are unattended: no human can answer a prompt here. The tools that would wait
for one are disabled. If a task is ambiguous, make a reasonable assumption, state
it on the channel, and proceed — never stop to wait for input.
EOF

# --- disallowed tools -------------------------------------------------------
# A containerized worker is unattended by construction, so any tool that waits
# for human input can only hang it: AskUserQuestion has no one to answer, and
# ExitPlanMode matters only in a plan mode a skip-permissions worker never
# enters. Block both by default. The `-` (not `:-`) means UNSET → default while
# an explicitly EMPTY AGENT_CHAT_DISALLOWED_TOOLS opts out entirely; any other
# value overrides the list. The names are bare tokens (no shell metachars), so
# the flag is built unquoted on purpose to split into separate args. It MUST come
# AFTER the positional prompt on the claude command line: --disallowed-tools is
# variadic and otherwise swallows the prompt, parsing each prompt word as a bogus
# deny rule and leaving the session with no instructions (verified, claude v2.1).
DISALLOWED_TOOLS="${AGENT_CHAT_DISALLOWED_TOOLS-AskUserQuestion ExitPlanMode}"
DISALLOW_ARGS=""
[[ -n "$DISALLOWED_TOOLS" ]] && DISALLOW_ARGS="--disallowed-tools $DISALLOWED_TOOLS"
log "disallowed tools: ${DISALLOWED_TOOLS:-<none>}"

# --- launch -----------------------------------------------------------------
# tmux supplies the pty an interactive TUI needs while running detached. The
# session runs as the container's foreground concern; we block on its liveness.
# $DISALLOW_ARGS trails the prompt (see the variadic note above).
log "launching worker '${WORKER_NAME:-<auto-named>}' on channel '$AGENT_CHAT_CHANNEL' (root $CHANNEL_ROOT)"
tmux new-session -d -s worker \
  "claude --dangerously-skip-permissions \"\$(cat '$SEED_FILE')\" $DISALLOW_ARGS; \
   printf '\\n--- claude session exited (rc=%s) ---\\n' \$?; sleep 3"

log "session started in tmux 'worker'. Attach with: docker exec -it <container> tmux attach -t worker"

# Minimal-proof blocker: hold the container open while the session lives. No
# relaunch — when claude exits, the container exits (supervisor is follow-up).
while tmux has-session -t worker 2>/dev/null; do
  sleep 5
done
log "tmux session ended; entrypoint exiting"
