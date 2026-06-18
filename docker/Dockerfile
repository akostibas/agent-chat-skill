# agent-chat containerized worker.
#
# A persistent, interactive Claude Code session that joins a channel and idles,
# driven across turns by Monitor notifications. See docker/README or the repo
# README "Containerized worker" section for build/run/secrets.
#
# Secrets are NEVER baked in: the Claude credentials and any GitHub token are
# supplied at run time (mount/env). `docker history` on this image shows none.

# --- Stage 1: build the agent-chat binary for linux -------------------------
# Go 1.25 (≥1.25.9): builds the go1.23 agent-chat module fine, and — more to the
# point — the same toolchain is copied into the runtime so the worker can build
# the projects it's dispatched. A target workload pins `go 1.25.9` in its go.mod,
# so the baked toolchain MUST be ≥1.25.9 or its build hard-fails (with
# GOTOOLCHAIN=local) or silently downloads (with =auto). This deliberately tracks
# the workload, overriding the usual previous-major default.
FROM golang:1.25-bookworm AS build
WORKDIR /src
# Module files first for layer caching, then sources. The CLI imports the
# `channel` package, so both source trees are needed (stdlib only — no go.sum).
COPY go.mod ./
COPY channel ./channel
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -o /out/agent-chat ./cmd/agent-chat/

# --- Stage 2: runtime -------------------------------------------------------
# node:24 (current LTS) — both stages are bookworm so the copied Go toolchain is
# ABI-compatible with the runtime's glibc.
FROM node:24-bookworm-slim

# Runtime + task toolchain. The worker runs as a non-root user with no sudo, so
# everything a dispatched task might need must be baked in here (issue #8 audit):
#   tmux              pty for the unattended TUI
#   git               clone/commit/push (private repos via injected PAT)
#   jq, bash, ca-certificates, curl  skill + common-task deps
#   make, python3     common needs for Go-with-make projects and scripts
# Go itself is copied from the build stage below (apt's golang is too old).
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        tmux git jq ca-certificates bash curl make python3 \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code \
    && claude --version

# Bake in the Go toolchain (same version that built the binary) so the worker
# can build/test Go projects without root or on-task apt.
# GOTOOLCHAIN=auto (the default, set explicitly so shipped behavior is known):
# the baked ≥1.25.9 satisfies the target workload with no download; if a
# workload's go.mod later requires a newer Go, it auto-downloads rather than
# hard-failing.
COPY --from=build /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" \
    GOPATH=/home/node/go \
    GOTOOLCHAIN=auto
RUN go version && make --version | head -1

# Install the skill exactly as `make install` does, but into the image's HOME.
# node:22 ships a uid-1000 `node` user; the worker runs as it.
ENV SKILL_DIR=/home/node/.claude/skills/agent-chat
COPY skill/ ${SKILL_DIR}/
COPY --from=build /out/agent-chat ${SKILL_DIR}/agent-chat
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x "${SKILL_DIR}"/*.sh "${SKILL_DIR}/agent-chat" /usr/local/bin/entrypoint.sh \
    && printf 'docker\n' > "${SKILL_DIR}/VERSION" \
    && mkdir -p /home/node/.claude /workspace \
    && chown -R node:node /home/node /workspace

USER node
WORKDIR /workspace

# Default channel root is the bind-mount point; override at run time as needed.
ENV AGENT_CHAT_ROOT=/channel \
    AGENT_CHAT_SKILL_DIR=${SKILL_DIR} \
    HOME=/home/node

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
