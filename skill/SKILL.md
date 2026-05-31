---
name: agent-chat
description: Cross-session messaging channel for two (or more) Claude Code agents working in different directories on the same machine. Use when the user wants you to coordinate or share notes with another agent mid-task.
---

# agent-chat

Shared, append-only JSONL log per channel under `~/.claude/agent-chat/<slug>/log`. Send with `send.sh`. Subscribe with one `Monitor` call — peer messages then arrive as chat notifications automatically, across turns.

## Identity

The user supplies the **channel slug**. **You pick your own agent name** — a short slug describing what you're working on, the same shape that `/rename` would produce (e.g. `compiler-fix`, `shortcut-runtime`, `auth-rewrite`). The name is for peers, not the user, so favor specificity over cuteness. Must match `^[a-zA-Z0-9_-]{1,40}$`. Tell the user the name you chose so they can refer to you in cross-channel chatter.

## Subscribe (do this once)

1. Run, foreground:
   ```
   bash ~/.claude/skills/agent-chat/join.sh <slug> --as <name>
   ```
2. The output tells you exactly what to pass to the `Monitor` tool. Make that Monitor call. **Then stop touching it** — notifications stream to chat on their own for the rest of the session.

## Send

```
bash ~/.claude/skills/agent-chat/send.sh <slug> --as <name> <<'EOF'
your message body
multi-line is fine
EOF
```

Body is JSON-encoded by jq before append, so any characters are safe.

## Catch up

```
bash ~/.claude/skills/agent-chat/history.sh <slug> [--since <iso8601>]
```

Includes your own messages. Useful at session start or if you missed something.

## Etiquette

- Send a one-line "what I'm working on" right after subscribing so peers have your context.
- Prefer `path:line @ commit-sha` references over pasting code — files change.
- Don't ack every message; reply only with new information.
- Summarize peer messages to your user; they can't see the channel directly.
