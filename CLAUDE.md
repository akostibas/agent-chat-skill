# agent-chat

A Claude Code skill that lets two (or more) Claude Code sessions on the same
machine exchange notes mid-task. A single Go binary (stdlib only) backs the
skill; the shell files in `skill/` are thin shims that exec it.

## Layout

- `channel/` — the importable core: the `Record` schema, flock protocol,
  presence, mention resolution, and the byte-offset cursor. A **supported,
  SemVer-governed** Go package (`github.com/akostibas/agent-chat-skill/channel`)
  that external Go peers import; the CLI is a thin layer over it. See ADR-0005.
- `cmd/agent-chat/` — the CLI (`join`/`send`/`history`/`stream`) over the
  package, plus CLI-only concerns (arg parsing, git cwd/branch, channel sweep,
  update nudge, output formatting).
- `skill/` — the skill itself: `SKILL.md` plus the `join.sh`/`send.sh`/
  `history.sh`/`stream.sh` shims that exec the binary.
- `bin/` — workflow scripts (`smoke-test.sh`, `release.sh`).
- `Makefile` — `build`, `install` (copies `skill/` + the built binary to
  `~/.claude/skills/agent-chat/`), `unit` (`go test -race ./...`), `test`
  (unit + smoke). Override the destination with `SKILL_DIR=...`.
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

Before cutting a release, add a `## vX.Y.Z` section to `CHANGELOG.md` with a
bullet list of new features / bug fixes — `bin/release.sh` uses that section
verbatim as the GitHub release body.

Cut releases with `bin/release.sh <version|patch|minor|major>`. It refuses on
a dirty tree, wrong branch, out-of-sync `main`, an existing tag, a missing
`CHANGELOG.md` section, or a failing smoke test, then tags, pushes, and creates
a GitHub release from the changelog notes. Never tag by hand — the script is
the gate that guarantees the smoke test passed against the published commit.
