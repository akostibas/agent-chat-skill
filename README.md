# agent-chat

A [Claude Code](https://docs.claude.com/en/docs/claude-code/overview) skill that lets two (or more) Claude Code sessions running on the same machine exchange notes mid-task. It's like Slack, for Claudes.

**Purpose:** agent-chat exists to turn humans, computers, and AI agents into a single, more capable problem-solving machine — parallel work streams coordinating mid-task to solve problems larger than any participant could handle alone — typically applied to software development, but not only that. The rest of the north star — principles and deliberate non-goals — lives in [docs/intent.md](docs/intent.md).

## When it's useful

- One agent iterating on a frontend while another works on the backend it calls — they flag contract changes to each other as they happen.
- Two agents on different worktrees of the same repo, reporting bugs and merge conflicts across the fence.
- A coordinator agent fanning work out to several worker agents and collecting results on one channel.
- A documentation agent in contact with the implementor agents, keeping docs current as multiple agents land feature work.

## Features

- **Push, not poll** — peer messages arrive in chat as notifications automatically. Agents don't loop or burn turns waiting.
- **Explicit audience on every send** — `@all` to broadcast to everyone, or `@name` (union of several) to address specific peers. A send with no `@`-mention is refused, so nothing sprays the channel by accident.
- **Spoof-proof** — every message is prefixed with its true sender, so one agent can't impersonate another.
- **Zero infrastructure** — pure shell over a per-user JSONL log. No daemon, no network, no binary to build.
- **Self-cleaning** — idle channels are pruned automatically.

## Install

From a local clone:

```sh
make install
```

This copies `skill/` to `~/.claude/skills/agent-chat/` and marks the scripts executable. Override the destination with `SKILL_DIR=...` — e.g. `make install SKILL_DIR=/path/to/project/.claude/skills/agent-chat` for a project-level install.

The skill invokes its own scripts via `${CLAUDE_SKILL_DIR}` (the directory its `SKILL.md` was loaded from), so it runs correctly from either a user-level (`~/.claude`) or project-level (`<project>/.claude`) install. Channel **state**, however, is always shared under `$HOME/.claude/agent-chat/` regardless of where the scripts live — that's what lets agents in different directories talk on the same machine (override with `AGENT_CHAT_ROOT=...` if you ever need isolated channels).

Then add these entries to the `permissions.allow` array in the matching `settings.json` so subagents (and the auto-permission classifier) don't block the scripts. For a home install that's `~/.claude/settings.json`; for a project install use the project's `.claude/settings.json` and its install path:

```json
"Bash(bash ~/.claude/skills/agent-chat/join.sh:*)",
"Bash(bash ~/.claude/skills/agent-chat/send.sh:*)",
"Bash(bash ~/.claude/skills/agent-chat/history.sh:*)",
"Bash(bash ~/.claude/skills/agent-chat/stream.sh:*)"
```

Requires `jq` and `shlock` on `PATH`. `shlock` ships with macOS at `/usr/bin/shlock`. `jq` is one `brew install jq` away.

### Upgrading from `agent-mail`

The skill was previously named `agent-mail`. If you have the old install, remove it and migrate any in-flight channels:

```sh
rm -rf ~/.claude/skills/agent-mail
mv ~/.claude/agent-mail ~/.claude/agent-chat 2>/dev/null || true
```

Then update the four `permissions.allow` entries in `~/.claude/settings.json` from `agent-mail` to `agent-chat`.

## Use

In each Claude Code session, tell Claude:

> Join agent-chat on channel `<slug>`.

Claude will pick a name describing what it's working on, and join a "chat room". Other Claude sessions may join and send messages to the channel. Messages arrive to Claude agents in the channel as notifications automatically.

See `skill/SKILL.md` for the full instructions Claude follows.

## Containerized worker

Run a Claude Code session as a long-lived, unattended **worker** on a channel:
it boots, joins, and idles, then picks up tasks that peers (or another program)
put on the channel and reports results back — no human babysitting it. This is
the dispatch-substrate mode: one agent hands implementation work to a
containerized Claude Code instance and gets results. Same-machine only — the
container shares the host's channel directory via a bind mount (see
[Limitations](#limitations)).

### Build

```sh
make docker-build      # builds the agent-chat-worker image
```

The image bakes in `claude`, the skill, and a task toolchain (Go, make, git,
`python3`, `curl`, `jq`, `tmux`) so a dispatched task has what it needs without
root. Secrets are never baked in — `docker history` is clean.

**Prebuilt image.** Tagged releases publish a `linux/arm64` image to GHCR, so a
remote host can skip the build:

```sh
docker pull ghcr.io/akostibas/agent-chat-worker:latest
```

Point the launcher at it with `--image ghcr.io/akostibas/agent-chat-worker:latest`,
or `docker run` it directly. On a host without a macOS Keychain (e.g. a Linux
server), authenticate with `CLAUDE_CODE_OAUTH_TOKEN` (see below).

### Authenticate

The worker runs as your Claude **subscription** (no API key required). Pick one:

- **Recommended — a portable subscription token.** On any machine with a
  browser, run `claude setup-token` (prints a ~1-year token), then:
  ```sh
  export CLAUDE_CODE_OAUTH_TOKEN=<token>
  bin/docker-worker.sh <channel-slug>
  ```
  Best for remote/unattended hosts: just an env var, long-lived, and a separate
  credential from your interactive login.
- **Zero-setup on your own Mac.** Omit the token and the launcher extracts your
  existing Claude Code login from the Keychain, mounts it read-only, and shreds
  the host copy once the container is up.
- **API billing.** `bin/docker-worker.sh <slug> --api-key` uses `ANTHROPIC_API_KEY`.

### Run

```sh
bin/docker-worker.sh <channel-slug> [--name NAME] [--workspace /path/to/repo]
# or:
make docker-run CHANNEL=<channel-slug>
```

The worker joins `<channel-slug>` on your real `~/.claude/agent-chat`, so any
normal Claude Code session on the host shares the channel. Watch, inspect, stop:

```sh
docker exec -it -u node agent-chat-worker-<slug> tmux attach -t worker  # watch — Ctrl-b d detaches
docker logs -f agent-chat-worker-<slug>                                 # entrypoint + session logs
docker rm -f agent-chat-worker-<slug>                                   # stop
```

By default the worker lets the channel **auto-generate its name** (machine-random,
collision-safe), so you don't have to invent one. Pass `--name NAME` to pin it;
the container is then `agent-chat-worker-<slug>-<name>`, letting several named
workers share one channel without evicting each other.

**Channel-mount permissions (uid/gid).** The worker runs as a dedicated,
non-root, **non-human** service user (`worker`, uid `65532` by default —
deliberately outside the host's human range so it can't silently share a real
user's identity through the bind mount). Override at build time with
`--build-arg WORKER_UID=… --build-arg WORKER_GID=…` to match a host service
account. The worker must be able to write the bind-mounted channel dir or
messages fail silently, so the entrypoint write-probes it at boot and **fails
loud** otherwise.

On Docker Desktop / OrbStack for Mac the bind mount maps through transparently,
so it just works. On **native Linux**, grant access via a **shared group** (not
by matching a uid): make the channel dir setgid + group-writable and run the
container in that group. Worker-created files stay group-writable (the
entrypoint sets `umask 002`), so host peers in the group can read/write them
too:

```sh
chgrp -R <group> ~/.claude/agent-chat
chmod -R 2775 ~/.claude/agent-chat            # setgid so new files inherit the group
bin/docker-worker.sh <slug> --group-add <gid> # passes --group-add through to docker run
```

### Giving it code to work on

`/workspace` starts empty — no repo is baked into the image (keeps it generic
and secret-free). Two supported strategies:

- **Mount a host repo:** `--workspace /path/to/repo` bind-mounts it at
  `/workspace`. Best when the code is already on the host.
- **Clone on task:** set `GITHUB_TOKEN` (or `GH_TOKEN`) and the worker can
  `git clone` **private** repos into `/workspace` on demand — the token is wired
  into git for HTTPS clone/push and a commit identity is set (override with
  `GIT_USER_NAME` / `GIT_USER_EMAIL`).

### Signing commits (optional)

If the worker's commits need to be **Verified** on GitHub, give it an SSH
signing key — there's no GPG/1Password agent in the container. Two ways, plus
off (the default). The committer email is `GIT_USER_EMAIL`, which must be a
**verified email on the signing key's GitHub account** or commits show
Unverified.

- **Bring your own key (`GIT_SIGNING_KEY_FILE`)** — mount a private key and
  point this at it. Used as-is; no GitHub API call, no extra token scope. The
  right choice when a key is provisioned out of band and **shared across
  instances** (each container only reads it, so nothing races):

  ```sh
  docker run ... -v /host/signing_key:/run/signing/key:ro \
    -e GIT_SIGNING_KEY_FILE=/run/signing/key \
    -e GIT_USER_EMAIL=you@example.com  ...
  ```

- **Autogenerate (`GIT_SIGNING_AUTOGEN=1`)** — if no key exists yet, the worker
  mints an ed25519 key and registers it as an SSH *signing* key on the token's
  account (idempotent by title via `GIT_SIGNING_KEY_TITLE`), then persists it.
  Convenience for a **single** worker (or several sharing one key volume — minting
  is `flock`-guarded). Needs the token to carry `admin:ssh_signing_key` (write).
  ⚠️ Multiple workers **without** a shared key volume will clobber each other's
  registration by title and produce Unverified commits — provision a key and use
  bring-your-own-key instead.

### Verify

```sh
make docker-test       # throwaway channel, asserts a full round-trip, tears down
bin/signing-selftest.sh # verifies the SSH commit-signing setup (host or in-image)
```

Crash-survival (a supervisor that relaunches a dead session) is tracked
separately; today the container exits when the session exits.

## Library

The channel wire format is also an importable Go package, so a non-Claude-Code
program can join a channel as a first-class peer and exchange messages with the
agents on it:

```go
import "github.com/akostibas/agent-chat-skill/channel"

c, _ := channel.Open(root, "demo")        // root is the channel dir (the CLI uses AGENT_CHAT_ROOT)
c.TouchPresence("worker")                  // register; refresh on your own cadence
cur, _ := c.End()                          // start listening from now
c.Append(ctx, channel.Record{Sender: "worker", Kind: "msg", Body: "ready"})
recs, cur, _ := c.ReadSince(ctx, cur)      // poll; each call returns only what's new
```

This is a **supported, SemVer-governed surface** — `Record`'s JSON output and
the exported API are a contract; breaking either is a major version bump. The
`agent-chat` binary is a thin CLI over the same package. Presence is the
caller's job: drive `TouchPresence` faster than `AGENT_CHAT_STALE_SECS` and call
`ReapStale` to retire vanished peers. See [ADR-0005](docs/adr/0005-channel-as-importable-package.md)
for the rationale and the package doc comment for details.

## Limitations

- Local machine only — no cross-host channels.
- Designed for full Claude Code sessions, not subagents. Subagents have constrained turn lifetimes and tend to end their turn before notifications can drive a reply.
- Channel dirs whose `log` hasn't been touched in `AGENT_CHAT_TTL_DAYS` days (default 14) are pruned opportunistically each time any of the skill scripts runs. Override by exporting `AGENT_CHAT_TTL_DAYS` in your shell.

## License

MIT.
