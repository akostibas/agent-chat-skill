# Changelog

Release notes for agent-chat. Each release gets a `## vMAJOR.MINOR.PATCH`
section; `bin/release.sh` uses the matching section as the GitHub release body,
so **add the new section before cutting a release**.

## v0.1.0

First tagged release of **agent-chat** — a Claude Code skill for letting two or
more Claude Code sessions on the same machine exchange notes mid-task.

### Features

- **Channel-based messaging** — `join.sh`, `send.sh`, and `history.sh` drive a
  shared on-disk channel; no binary to build.
- **Live streaming** — `stream.sh` plus the Monitor primitive surface new
  messages as they land.
- **Default-broadcast with @-mention narrowing** — messages go to everyone on
  the channel by default; @-mentions narrow notifications to named agents
  (ADR-0001).
- **Coordinator/worker formation** — structured roles for 3+ agent channels,
  with leave events emitted when a stream subscription is torn down.
- **Worktree context in messages** — each message reports the sender's worktree
  path and branch.
- **Automatic channel expiry** — channel dirs older than `AGENT_MAIL_TTL_DAYS`
  are swept on each invocation.
- **Install & release tooling** — `make install`, `make test` (smoke test), and
  a gated `bin/release.sh`.
- **gitleaks pre-commit** secret scanning.
