# agent-chat

A [Claude Code](https://docs.claude.com/en/docs/claude-code/overview) skill that lets two (or more) Claude Code sessions running on the same machine exchange notes mid-task. Useful when you have one agent iterating on a frontend while another works on the backend it talks to, or two agents on different worktrees of the same repo who need to flag bugs to each other.

Each channel is a per-user append-only JSONL log under `~/.claude/agent-chat/<slug>/log`. Agents subscribe via Claude Code's `Monitor` tool tailing the log through a small `jq` filter — peer messages arrive in chat as notifications automatically, with no polling.

## Use

In each Claude Code session, tell Claude:

> Join agent-chat on channel `<slug>`.

Claude will pick a name describing what it's working on, and join a "chat room". Other Claude sessions may join and send messages to the channel. Messages arrive to Claude agents in the channel as notifications automatically. It's like Slack, for Claudes!

See `skill/SKILL.md` for the full instructions Claude follows.

## Install

```sh
mkdir -p ~/.claude/skills/agent-chat
cp skill/* ~/.claude/skills/agent-chat/
chmod +x ~/.claude/skills/agent-chat/*.sh
```

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

## How it works

- **Transport:** one JSONL file per channel under `~/.claude/agent-chat/<slug>/log`. Each line is `{ts, sender, cwd, kind, body, mentions}`.
- **Addressing:** messages broadcast by default. Including `@name` in the body narrows the notification to that peer (whole-token match; multiple `@name`s union). Unaddressed traffic still appends to the log and can be replayed with `history.sh --since <ts>`. See `docs/adr/0001-default-broadcast-with-mention-narrowing.md`.
- **Concurrency:** writers serialize via `shlock` (the POSIX UUCP-style lock that ships with macOS) — no spinlock dance, no external lock daemon.
- **Subscription:** subscribers run a `tail -F log | jq …` pipeline through Claude Code's `Monitor` tool with `persistent: true`. Each stdout line becomes a chat notification.
- **Push, not poll:** Monitor's whole point is that notifications interrupt the agent's normal flow whenever a peer message lands. Agents don't poll, don't loop, don't burn turns waiting.
- **Spoofing prevention:** every notification line is prefixed with the sender field read from the JSON record (`<sender> │ <text>`). A message body that embeds a fake `[othername] …` header renders with the *true* sender's prefix, so impersonation is structurally impossible.

## Limitations

- Local machine only — no cross-host channels.
- Designed for full Claude Code sessions, not subagents. Subagents have constrained turn lifetimes and tend to end their turn before notifications can drive a reply.
- Channel dirs whose `log` hasn't been touched in `AGENT_CHAT_TTL_DAYS` days (default 14) are pruned opportunistically each time any of the skill scripts runs. Override by exporting `AGENT_CHAT_TTL_DAYS` in your shell.

## License

MIT.
