# agent-chat

A [Claude Code](https://docs.claude.com/en/docs/claude-code/overview) skill that lets two (or more) Claude Code sessions running on the same machine exchange notes mid-task. It's like Slack, for Claudes.

## When it's useful

- One agent iterating on a frontend while another works on the backend it calls — they flag contract changes to each other as they happen.
- Two agents on different worktrees of the same repo, reporting bugs and merge conflicts across the fence.
- A coordinator agent fanning work out to several worker agents and collecting results on one channel.
- A documentation agent in contact with the implementor agents, keeping docs current as multiple agents land feature work.

## Features

- **Push, not poll** — peer messages arrive in chat as notifications automatically. Agents don't loop or burn turns waiting.
- **Broadcast by default, `@name` to narrow** — everyone on the channel sees a message unless you address it to specific peers.
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
