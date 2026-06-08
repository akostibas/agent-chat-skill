# Changelog

Release notes for agent-chat. Each release gets a `## vMAJOR.MINOR.PATCH`
section; `bin/release.sh` uses the matching section as the GitHub release body,
so **add the new section before cutting a release**.

## v0.3.0

### Features

- **Self-update.** New `skill/update.sh` upgrades the installed skill in place:
  it resolves its own install dir (so it works for both `~/.claude` and
  project-level `.claude` installs), clones the latest GitHub release tag into
  `$TMPDIR`, runs `make install` against that dir, and removes the checkout. No
  files are written outside the system temp dir. Run it bare to see a
  `current → latest` plan and confirm, or `--yes` to apply unattended.
- **Update nudge.** `_common.sh` now does a throttled, best-effort check (at
  most once per `AGENT_CHAT_UPDATE_TTL_SECS`, default 24h) comparing the
  installed version against the latest release tag, printing a one-line stderr
  hint when behind. It never auto-applies, and never breaks send/join/stream on
  network failure. Opt out with `AGENT_CHAT_NO_UPDATE_CHECK=1`.
- `make install` now writes a `VERSION` file (from `git describe --tags`) next
  to the scripts, which is the version source both mechanisms read.

## v0.2.2

### Fixes

- **Relocatable installs.** The skill no longer hardcodes `~/.claude/skills/agent-chat`
  in its own invocation paths, so a project-level install
  (`<project>/.claude/skills/agent-chat`) actually runs its own copy. `SKILL.md`
  now invokes the bundled scripts via `${CLAUDE_SKILL_DIR}` (resolves to
  whichever install is active), and `join.sh` prints a `Monitor` command that
  references its own resolved directory rather than a fixed home path. Channel
  state remains `$HOME`-scoped, so cross-directory messaging is unchanged.

## v0.2.1

### Fixes

- **`@mentions` now resolve against the live channel roster.** Previously any
  `@token` in a message body narrowed delivery, so a literal scoped package
  name like `@vercel/otel` (or a typo'd peer name) registered as a mention and
  silently excluded every real peer from the notification — the message landed
  in the log but reached no one. `send.sh` now keeps only `@tokens` that name a
  currently-present member (from the presence dir); an unrecognized token
  leaves the mentions empty, so the message broadcasts. Unrecognized `@`
  degrades to over-delivery (broadcast), never a silent drop.

## v0.2.0

### Fixes

- **Reliable departure events.** A `leave` is now posted even when a session is
  hard-killed. Claude Code's `Monitor` `SIGKILL`s the stream child on session
  close, so the old `INT/TERM/HUP` trap never fired on the common teardown path.
  Streaming agents now heartbeat a presence file; a surviving peer reaps any
  agent whose heartbeat goes stale and posts the `leave` on its behalf. The
  graceful-signal trap is kept as a fast path. See
  `docs/adr/0003-presence-heartbeat-for-departure.md`.

### Features

- New `AGENT_CHAT_HEARTBEAT_SECS` (default 15) and `AGENT_CHAT_STALE_SECS`
  (default 45) tune the liveness/reaping windows.
- `bin/teardown-probe.sh` — a diagnostic harness for observing how `Monitor`
  tears down a persistent child.

### Notes

- Adds a `presence/` subdir to each channel directory. Additive and
  backward-compatible: an agent running the old scripts simply doesn't heartbeat
  and is never reaped (degrades to the prior no-sign-off behavior).

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
