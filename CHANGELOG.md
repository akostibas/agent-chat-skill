# Changelog

Release notes for agent-chat. Each release gets a `## vMAJOR.MINOR.PATCH`
section; `bin/release.sh` uses the matching section as the GitHub release body,
so **add the new section before cutting a release**.

## v1.5.0

### Features

- **`leave.sh` / `agent-chat leave` — deliberate mid-session departure.** Posts
  your `[leave]`, removes presence, and stops all further delivery, including
  the hook path. Hook-subscribed agents previously had no way to leave a
  channel before their session ended (and a bare presence removal would
  self-heal back into membership on their next tool call); now they do.

## v1.4.0

### Features

- **Messages now reach you while you work — no Monitor, no wait loop.** A
  Claude Code hook (registered once, user-wide, by `make install`) injects new
  channel messages into a subscribed session's context between tool calls, so
  an urgent "stop" lands before the next write instead of ~90 seconds after
  the damage (the #56 incident). Joining is now subscribing: `join` registers
  the session and tells you which world you're in; Monitor and `wait.sh`
  remain for hook-less sessions and for blocking on a reply while idle. The
  hook shares the read frontier with the stream/wait paths (exactly-once), is
  a one-stat no-op for sessions that never joined, guards itself so an
  uninstalled binary exits silently, posts your clean leave at session end,
  and heartbeats your presence on every tool call — a working agent is no
  longer falsely reported as departed (#58). See ADR-0014.
- **`agent-chat hook install`** — idempotent, merge-preserving registration of
  the delivery hook in `~/.claude/settings.json` (run automatically by
  `make install`).
- **`channel` package (minor):** exported `RejoinBody` and `StaleSecs`, so
  consumers can pin sleep-flap semantics to the same constants the reaper
  uses.

## v1.3.0

### Fixes

- **You can now write an `@`-token without addressing anyone: wrap it in
  backticks.** A token inside a markdown code span is quoted content, not an
  address, so `` `@dependabot rebase` `` and `` `@vercel/otel` `` send
  first-try instead of being refused as mentions of an absent peer. Fenced
  blocks quote the same way. Previously there was no escape at all, so a
  routine status update about a bot or a scoped package had to be reworded
  until it went through — agent-chat's own ADR text could not be pasted into
  an agent-chat channel. Quoting is per-token, so a genuinely typo'd peer name
  is still refused (ADR-0010 does not regress), and an unterminated backtick is
  literal text that quotes nothing — keep backticks balanced, since a stray one
  pairs with a later quote's opening backtick and un-quotes that token (which
  refuses loudly; it never mis-delivers). The refusal message now names the escape
  and quotes the offending token back, leading with `Did you mean @x?` when the
  token is a near miss for a present member. (#57, ADR-0013.)

### Package

- **New:** `channel.CodeSpanMask(body)` reports which bytes sit inside a
  markdown code span. Peers that resolve addressing themselves should adopt it
  — without the same notion of "quoted" they will disagree with the CLI about
  who a message addressed.
- **Changed:** `channel.ExtractMentions` no longer returns tokens written
  inside a code span. This is the only behavior change, and it only ever
  *drops* backticked tokens; nothing new is returned.

## v1.2.0

### Features

- **Subscribing no longer requires the Monitor tool.** New `wait` subcommand
  (and `wait.sh` shim): a one-shot consumer that blocks — holding your
  presence heartbeat — until a wake-worthy peer message arrives, prints it,
  and exits. Sessions without Monitor (it's feature-flag gated; `DO_NOT_TRACK`
  or `DISABLE_TELEMETRY` in the env disables it) run it via a background Bash
  command and re-arm it after every exit. Each wait resumes from the previous
  one's persisted read frontier, so messages landing between exit and re-arm
  are still delivered. `join` now prints the fallback command alongside the
  Monitor instructions, and `channel` gains an exported `ReadOffset` getter.

## v1.1.0

### Features

- **A message to a peer who leaves before reading it now bounces back to you.**
  If you address a peer with `@name` and their session dies before it reads the
  message, you get a bounce notice when that peer is dropped from the channel —
  so handing a task to a worker that quietly crashed fails loudly instead of
  leaving you waiting on a reply that can never come. Only directed messages
  bounce; an `@all` broadcast is fire-and-forget. See ADR-0011.
- **A send with no `@`-mention is now a pull-only FYI, not a refusal.** It is
  recorded on the channel and appears when a peer catches up, but it wakes no
  one — the quiet tier for status, progress notes, and "heads up" breadcrumbs
  that are worth logging but not worth interrupting anyone for. A mis-addressed
  send (an `@name` that matches no present member) is still refused, since that
  is a likely typo rather than a deliberate note. See ADR-0012 (amends
  ADR-0010).

## v1.0.0

### Breaking changes

- **Every send must now name a reachable audience.** A send is refused (exit 2,
  with an actionable error) unless it contains `@all` or at least one `@name`
  that matches a present member. Previously a message with no `@`-mention
  silently broadcast to the whole channel — the main source of coordination
  noise in multi-agent fleets. `@all` is the new reserved, case-insensitive
  broadcast keyword. An `@name` that matches no present member (a typo, a package
  name like `@vercel/otel`, or a peer that hasn't joined) is now refused rather
  than "degrading" to a broadcast, so nothing sprays the channel by accident and
  nothing silently vanishes. Callers that relied on bare broadcast (the
  SessionStart beacon, the docker idle-worker announcement) must add `@all`. See
  ADR-0010 (supersedes ADR-0001).

## v0.16.0

### Bug fixes

- **Host sleep no longer spams peers with fake "left / reconnected" churn.** On
  a Mac that sleeps and wakes on its own (idle naps, the ~15-minute maintenance
  wake), a still-running agent was being briefly announced as gone and then
  rejoining — over and over on a long session. Every one of those pairs woke the
  *other* agents to react to a peer that never actually left, burning a turn on a
  non-event. Two things caused it and both are fixed: the "we just woke up, don't
  evict anyone yet" guard was reading a clock that doesn't advance while the Mac
  is asleep, so it never triggered — it now measures elapsed real time and
  correctly rides out a wake; and a plain `send` used to sweep for departed peers
  from a throwaway process that had no way to tell sleep apart from death, so it
  no longer sweeps (that's the always-on stream's job). As a backstop, a
  subscriber now briefly holds a "timed out" departure and drops it silently if
  that peer reconnects a few seconds later, so a sleep blip never reaches you as
  a notification while a genuine departure still does.

## v0.15.0

### New features

- **Agents stay online across host sleep.** When the machine slept, every
  streaming agent froze at once, so on wake their heartbeats all looked
  stale simultaneously — whichever agent's heartbeat fired first reaped all the
  others as "timed out," and the falsely-reaped-but-still-running agents never
  came back on their own (their routine heartbeat refused to recreate a deleted
  presence file), so you had to manually convince each one to re-join. Presence
  is now self-healing and wake-aware: a running stream reasserts its own presence
  every beat — re-announcing a `[join]` if it had been reaped — and the first
  beat after a detected clock jump skips reaping so live peers get a chance to
  refresh before anyone is judged gone. A terminal that's still running Claude
  Code stays online in the chat. See ADR-0009.

## v0.14.0

### New features

- **Messages now arrive whole in peer notifications (#37).** The Claude Code
  harness clips Monitor events at ~600 rendered chars per line and ~2.5K per
  event (measured empirically with calibrated probes between two live
  sessions), which cut most substantive messages mid-word and forced a
  `history.sh` pull that paid for the body twice. The stream now wraps body
  lines escape-aware to 400 rendered chars and self-caps events at 2000;
  oversize messages are cut at a line boundary with a footer naming the exact
  `history.sh --since` command that recovers the full text. New etiquette:
  same-machine peers should exchange large artifacts as file paths.
- **`agent-chat update` (#38).** The self-update flow moved from ~100 lines of
  bash into the binary — same behavior (plan by default, `--yes` to apply,
  upgrades whatever install dir the binary runs from), and curl is no longer
  required. `update.sh` is now a thin shim like its siblings.

## v0.13.0

### New features

- **Feedback poll (#31).** Channels can now harvest agent-chat friction and
  process-improvement ideas from live agents and turn them into GitHub issues,
  rarely and with a human in the loop. See `docs/adr/0008-feedback-poll.md`.
  - **Trigger (#33).** The join that *creates* a channel rolls once
    (`AGENT_CHAT_FEEDBACK_RATE`, default `0.10`, `0` disables) and, on a hit,
    opens a feedback round atomically with the first join record. It's a
    once-per-channel decision — later joiners inherit it and never re-roll, so
    the rate is a true per-channel 10% rather than compounding across joiners.
    `join` nudges the agent to submit when (and only when) a round is open.
  - **Primitive (#32).** New `feedback open|submit|tally|close` subcommands (and
    a `feedback.sh` shim) over three additive, event-sourced record kinds
    (`poll-open`/`poll-submit`/`poll-close`) tagged with a new `round` field.
    `tally` returns the deduped candidate list; the `msg` wire schema is
    unchanged (the field is omitted when empty).
  - **Coordinator flow (#34).** `SKILL.md` guidance: one coordinator per round
    dedups against existing issues, gets explicit user approval, files one issue
    per item, and closes with a terminal outcome. Containerized workers with no
    `gh` access fall back to posting the list into the channel for a human.
  - New package API: `Channel.JoinNew` (Join delegates to it) plus the
    `FeedbackRound` operations, for external Go peers.

## v0.12.1

### Bug fixes

- **Presence sweep no longer re-emits `[leave]` or falsely reports live members
  as departed (#29).** Two related defects in the ADR-0003 heartbeat mechanism
  are fixed by separating presence *creation* from presence *refresh*:
  - A member's own heartbeat (and `send`) now uses a refresh-only path that
    never re-creates a presence file the sweep has already reaped. Previously a
    `stream` reconnect after a reap silently recreated the file with no `[join]`,
    so the next sweep re-announced the same departure — observed ~11× for one
    peer. A reaped member now rejoins only via an explicit `join` (which logs a
    real `[join]`).
  - `send` now refreshes the sender's own heartbeat. An agent that is actively
    sending but whose `stream` subprocess has died (e.g. Monitor `SIGKILL`) is
    no longer falsely reaped as timed-out while it is demonstrably alive.

## v0.12.0

### Features

- **Coordinator-spawned worker fleet (#17).** A coordinator can now fan out a
  fleet of containerized workers and drive them unattended, instead of asking a
  human to open N sessions. Containers run `--dangerously-skip-permissions` and
  are unattended by construction, so this sidesteps the auto-mode-stall problem
  entirely — there's no mode to detect.
  - **`bin/spawn-fleet.sh -n N`** launches N hardened workers on a *private,
    ephemeral* channel (its own temp dir, not your global `~/.claude/agent-chat`),
    each cloning the target repo (`--repo`, default: the cwd's `origin`) fresh
    into `/workspace`. It labels every container `agent-chat-fleet=<id>` and
    prints the command to join the channel as coordinator plus the teardown line.
  - **`bin/teardown-fleet.sh <id>`** stops every container by that label and
    removes the ephemeral channel (use `--list` to see live fleets). It does not
    delete branches workers pushed — those may hold unmerged work.
  - Workers clone from GitHub and **push their branch back** for the coordinator
    to merge/PR; no host worktrees, no shared `.git` across the container.
  - `SKILL.md` gains a coordinator guide; see
    `docs/adr/0007-coordinator-spawned-worker-fleet.md`.
- **Interactive tools blocked in workers by default.** The worker entrypoint now
  passes `--disallowed-tools "AskUserQuestion ExitPlanMode"` to every session.
  An unattended container can't answer an interactive prompt, so these could only
  hang it. Override with `AGENT_CHAT_DISALLOWED_TOOLS` (set empty to allow all).
  **Behavior change:** existing image consumers pick this up on their next
  rebuild; it only removes tools that were already non-functional in a container.
- **`docker-worker.sh` gains `--clone`, `--label`, `--container`, and
  `--disallow`** (all additive) so it can serve as the fleet's per-worker
  primitive: clone a repo at boot, attach labels, set a container name decoupled
  from the channel name, and override the disallowed-tools list. The post-launch
  settle time is now the visible knob `AGENT_CHAT_LAUNCH_SETTLE_SECS` (default 4).

### Bug fixes

- **`docker-worker.sh` no longer exits 1 on a healthy start when using a token.**
  Its EXIT-trap cleanup used a `&&` chain that returned 1 whenever no Keychain
  temp file existed (the `CLAUDE_CODE_OAUTH_TOKEN` / `--api-key` auth paths),
  so the launcher reported failure despite the worker starting fine. The trap is
  now an `if` block that always exits 0.
- **Corrected the worker auto-mode guidance in `SKILL.md`.** It claimed a
  `## Auto Mode Active` system reminder lands in context when auto mode turns on;
  no such signal reliably exists (investigated in #17). Workers are now told they
  can't detect their mode from context and should confirm with their user at
  setup — or run as a container, which is unattended by construction.

## v0.11.1

### Bug fixes

- **Containerized workers auto-name by default (fixes a v0.11.0 regression).**
  The worker entrypoint defaulted its channel name to `container-worker` and
  always passed it as `--as`. After v0.11.0 made `join` reject an already-active
  name, two containers on one channel would fail the second join. The entrypoint
  and `bin/docker-worker.sh` now leave the name unset by default, so each
  container's `join` auto-generates a unique one; the seed prompt has the worker
  read back and adopt its assigned name. Pass `AGENT_CHAT_WORKER_NAME` /
  `--name NAME` to pin a name — `docker-worker.sh` then suffixes the container
  name (`agent-chat-worker-<slug>-<name>`) so several named workers can share a
  channel.

## v0.11.0

### Features

- **Collision-safe agent names (#16).** `join` no longer lets two sessions
  silently share one identity. Two changes work together:
  - **Race-free claim at join.** The new `channel.Join` claims the name *under
    the channel lock* — it reads the live roster, refuses a requested name
    that's already active (`ErrNameTaken`), and writes the presence file before
    releasing the lock. Claiming presence at join (not at stream start) closes
    the window where a just-joined peer was invisible, and staleness still
    applies so a name held only by a timed-out peer is reclaimable.
  - **Reject human-picked collisions; regenerate machine-picked ones.** A
    `--as` name that's already active makes `join` fail loudly so the agent
    re-picks (numeric suffixes confuse the humans watching the channel); an
    auto-generated collision just regenerates.
  - **Machine-owned entropy for default names.** `--as` is now optional. With no
    name, the binary generates a memorable `adjective-animal` name from
    `crypto/rand`, sidestepping the LLM name-clustering that caused the
    double-assignment in #16. SKILL.md now recommends omitting `--as` when
    joining cold.

## v0.10.0

### Features

- **Env-driven SSH commit signing for the containerized worker.** The worker
  does real git work but has no GPG/1Password agent, so its commits couldn't be
  Verified. The entrypoint now resolves a signing key from env at startup, in
  priority order: `GIT_SIGNING_KEY_FILE` (a mounted private key, used as-is —
  bring-your-own-key, no API call, safe to share one key across instances);
  `GIT_SIGNING_AUTOGEN=1` (mint an ed25519 key and register it as an SSH signing
  key on the token's account, idempotent by `GIT_SIGNING_KEY_TITLE`,
  `flock`-guarded — needs `admin:ssh_signing_key`); or off (default). Committer
  identity reuses `GIT_USER_NAME` / `GIT_USER_EMAIL`. The image now installs
  `openssh-client` (git's SSH signing shells out to `ssh-keygen`). New
  `bin/signing-selftest.sh` verifies the path on the host and inside the image.
  (issue #15)

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
