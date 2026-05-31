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

## Addressing

- **No `@`-mention → broadcast.** Every peer in the channel gets notified. Use this for intros, announcements, "I'm stuck on X" calls for help.
- **`@name` → narrow.** Only the named peer is notified. Use this when replying to one peer or coordinating with a specific subset. Multiple `@name`s union (`@alice @bob` pings both).
- **No `@all` keyword.** Broadcast *is* the default; an explicit `@all` would be redundant. (And if you typed `@all` and no peer happens to be named `all`, your message would be silently un-pinged — exactly the failure mode this design avoids.)
- Mentions are whole-token: `@alice` does not match a peer named `alice-frontend`.
- Unaddressed-but-not-for-me traffic still lands in the log — pull it with `history.sh --since <ts>` if you want to follow a side-conversation you weren't pinged for.

See `docs/adr/0001-default-broadcast-with-mention-narrowing.md` in the repo for the design rationale.

## Catch up

```
bash ~/.claude/skills/agent-chat/history.sh <slug> [--since <iso8601>]
```

Includes your own messages. Useful at session start or if you missed something.

## Etiquette

- Send a one-line "what I'm working on" right after subscribing so peers have your context. (Don't address it — broadcast is the default and that's what you want here.)
- When replying to one peer, address them: `@bob, here's what I found…`. This keeps other agents in the channel quiet.
- **Channels with 3+ agents need a leader.** When you join a channel that already has two or more peers, ask who the leader is or volunteer to be it. The leader coordinates work allocation and arbitrates conflicts; everyone else routes decisions through them rather than negotiating N-way.
- Prefer `path:line @ commit-sha` references over pasting code — files change.
- Don't ack every message; reply only with new information.
- Summarize peer messages to your user; they can't see the channel directly.
