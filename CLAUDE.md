# agent-chat

A Claude Code skill that lets two (or more) Claude Code sessions on the same
machine exchange notes mid-task. Pure shell — no binary to build.

## Layout

- `skill/` — the skill itself: `SKILL.md` plus `join.sh`, `send.sh`,
  `history.sh`, `stream.sh`, and the shared `_common.sh`.
- `bin/` — workflow scripts (`smoke-test.sh`, `release.sh`).
- `Makefile` — `install` (copies `skill/` to `~/.claude/skills/agent-chat/`
  and marks the scripts executable), `test` (runs the smoke test). Override
  the destination with `SKILL_DIR=...`.
- `docs/adr/` — architectural decision records.

## Testing

- `make test` (or `bin/smoke-test.sh`) — installs the skill into a throwaway
  `HOME`, then drives `join.sh`/`send.sh`/`history.sh` end-to-end against a
  scratch channel. Doesn't touch Monitor (that's a Claude Code primitive).
  No network, no real `~/.claude/` mutation.

## Versioning & releases

SemVer, tagged `vMAJOR.MINOR.PATCH`. While pre-1.0, breaking changes bump the
minor.

- **patch** — bug fixes, docs, internal refactors (no behavior change).
- **minor** — new backward-compatible flags or scripts.
- **major** — breaking changes to script names/flags, log line format, the
  on-disk channel layout, or the `Monitor` invocation contract.

Cut releases with `bin/release.sh <version|patch|minor|major>`. It refuses on
a dirty tree, wrong branch, out-of-sync `main`, an existing tag, or a failing
smoke test, then tags, pushes, and creates a GitHub release. Never tag by
hand — the script is the gate that guarantees the smoke test passed against
the published commit.
