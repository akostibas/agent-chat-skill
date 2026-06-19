# Changelog

Release notes for agent-chat. Each release gets a `## vMAJOR.MINOR.PATCH`
section; `bin/release.sh` uses the matching section as the GitHub release body,
so **add the new section before cutting a release**.

## v0.8.1

### Changed

- **Containerized worker: skill installed outside `$HOME`.** The worker image
  now bakes the skill at `/opt/agent-chat/skill`, and the entrypoint copies it
  into `$HOME/.claude/skills/agent-chat` at startup (where Claude Code discovers
  it). This lets the container run with a **read-only root filesystem** plus a
  writable HOME (tmpfs/volume) without the writable HOME shadowing the baked
  skill. Image-internal only — no change to the skill scripts, CLI, the
  `channel` package, or the on-disk channel format. (issue #14)

## v0.8.0

### Features

- **Read-only `channel.ActiveMembers()`.** Returns the members whose heartbeat
  is still fresh (presence file mod-time within `AGENT_CHAT_STALE_SECS`), using
  the same threshold `ReapStale` honors. Unlike `Members` it filters out stale
  peers, and unlike `ReapStale` it has no side effects (no leave records, no
  lock). Lets a consumer gate a feature on a peer actually being present without
  mutating the channel. Additive to the SemVer-governed `channel` surface.

## v0.7.0

### Features

- **Serializable `channel.Cursor`.** New `Cursor.Offset() int64` and
  `CursorAt(off int64) Cursor` let a long-lived consumer persist its read
  position to disk and restore it across restarts, surfacing each record exactly
  once. `ReadSince` already self-heals a stale offset past a shrunken log, so the
  restore path is safe across channel recreation. Additive to the SemVer-governed
  `channel` surface (issue #13).

### Changed

- **Worker runs as a dedicated non-human service uid (default 65532), not
  `node`/1000.** Running as uid 1000 — the typical first human login — meant the
  bind-mounted channel dir made the worker write as, and share the identity of,
  a real host user (while running with permissions skipped). The worker now uses
  a reserved service uid/gid (build-arg `WORKER_UID`/`WORKER_GID` to override),
  and channel-dir access is granted via a **shared group** (`--group-add`), not
  by uid collision. The entrypoint sets `umask 002` so worker-created channel
  files stay group-writable for host peers, and `bin/docker-worker.sh` gains a
  `--group-add` passthrough. Verified on native Linux: the boot write-probe
  fails loud without the group, passes with it (issue #12).

## v0.6.0

### Features

- **Containerized worker.** A `Dockerfile` + entrypoint run a persistent,
  interactive Claude Code session in a container that joins a channel and idles,
  picking up tasks dispatched on the channel and reporting back — the
  dispatch-substrate pattern (issue #8, ADR-0006). The session runs in tmux so
  it can hold a `Monitor` subscription across turns, and boots fully unattended
  (pre-accepts onboarding, folder-trust, and bypass-mode gates).
- **Run-time secrets, never baked in.** Auth via `CLAUDE_CODE_OAUTH_TOKEN`
  (`claude setup-token`, recommended), `ANTHROPIC_API_KEY`, or a mounted
  subscription-creds blob; a `GITHUB_TOKEN` is wired into git for private
  clone/push. `docker history` shows no secret.
- **Baked task toolchain.** The image ships `go` (1.25, matching the target
  workload), `make`, `python3`, `curl`, and `git` so a dispatched task can build
  real projects without root.
- **`make docker-build` / `docker-run` / `docker-test`** targets and a
  `bin/docker-worker.sh` launcher (extracts host Keychain creds on macOS,
  shreds the copy after boot). `docker-test` asserts a full round-trip.
- **GHCR publish workflow.** Tagged releases (`v*`) build and push a
  `linux/arm64` worker image to `ghcr.io/<owner>/agent-chat-worker` on GitHub's
  native arm64 runners, so a remote host can `docker pull` it instead of
  building.

## v0.5.0

### Features

- **Importable `channel` package.** The channel wire format — the `Record`
  schema, `log.lock` flock protocol, presence/heartbeat, and mention
  resolution — is now an importable Go package
  (`github.com/akostibas/agent-chat-skill/channel`), so an external Go program
  can join a channel as a first-class peer without reimplementing the format.
  `cmd/agent-chat` is a thin CLI over the same package; on-disk bytes are
  unchanged. This is a SemVer-governed surface — see ADR-0005.
- **Byte-offset cursor for polling.** `ReadSince(ctx, cur)` returns new records
  plus a cursor; `End()` starts a peer "from now". The cursor self-heals if the
  channel is deleted and recreated, and never drops or double-reads records that
  share a one-second timestamp (the failure mode a `--since <ts>` cursor has).
- **Schema golden test.** A test pins `Record`'s exact on-disk bytes (field
  order, omitempty, no HTML escaping) so wire-format drift is caught.

### Internal

- `make unit` now runs `go test -race ./...` across the whole module (the unit
  tests moved to the `channel` package with the core).

## v0.4.0

### Breaking changes

- **Requires Go to build.** `make install` now compiles a Go binary
  (`agent-chat`). The `go` toolchain must be on `PATH`. `shlock` and `jq` are no
  longer required at runtime.

### Features

- **Single Go binary replaces shell scripts.** `agent-chat join`, `send`,
  `history`, and `stream` subcommands replace `join.sh`, `send.sh`,
  `history.sh`, and `stream.sh`. Shell shims (`*.sh`) stay for this release as
  thin `exec` wrappers — existing agents and Monitor commands continue to work
  without change (COMPAT: shims will be removed in v0.5.x).
- **`flock(2)` replaces `shlock`.** Kernel-managed exclusive locking; locks are
  released automatically on process death. No more stale lock files.
- **Native JSON.** `encoding/json` replaces `jq` subprocesses for all
  serialisation and parsing.
- **Unit test suite.** `go test ./cmd/agent-chat/` covers lock contention, reap
  idempotency, mention extraction, roster filtering, record schema, and presence
  lifecycle. Run with `make unit`; the full `make test` runs unit then smoke.
- **Cross-platform.** `flock(2)` and Go stdlib work on macOS and Linux. The
  `stat`/`date` portability forks in `_common.sh` are gone.
- ADR-0004 documents the port decision and its tradeoffs.

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
