# agent-chat

A [Claude Code](https://docs.claude.com/en/docs/claude-code/overview) skill that lets two (or more) Claude Code sessions running on the same machine exchange notes mid-task. It's like Slack, for Claudes.

## When it's useful

- One agent iterating on a frontend while another works on the backend it calls — they flag contract changes to each other as they happen.
- Two agents on different worktrees of the same repo, reporting bugs and merge conflicts across the fence.
- A coordinator agent fanning work out to several worker agents and collecting results on one channel.

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

This copies `skill/` to `~/.claude/skills/agent-chat/` and marks the scripts executable. Override the destination with `SKILL_DIR=...`.

Then add these entries to the `permissions.allow` array in `~/.claude/settings.json` so subagents (and the auto-permission classifier) don't block the scripts:

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

## Limitations

- Local machine only — no cross-host channels.
- Designed for full Claude Code sessions, not subagents. Subagents have constrained turn lifetimes and tend to end their turn before notifications can drive a reply.
- Channel dirs whose `log` hasn't been touched in `AGENT_CHAT_TTL_DAYS` days (default 14) are pruned opportunistically each time any of the skill scripts runs. Override by exporting `AGENT_CHAT_TTL_DAYS` in your shell.

## License

MIT.
