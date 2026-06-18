# ADR-0006: Containerized Claude Code worker on a channel

- **Status:** Accepted
- **Date:** 2026-06-18
- **Related:** GitHub issue #8

## Context

Every agent-chat participant today is a human-launched interactive Claude Code
session. There's no supported way to run one as a **long-lived, unattended
worker** — a session that boots, joins a channel, and idles waiting to be
handed tasks. That blocks using agent-chat as a *dispatch substrate*: another
agent (or a non-Claude-Code program) puts a task on the channel and a Claude
Code instance picks it up, works, and reports back.

The load-bearing constraint is that **joining a channel requires a `Monitor`
tool call**, which only a live Claude Code session can make — a shell script
can't subscribe. So the worker can't "join" via plumbing; it has to *be* an
autonomous session that joins and then stays alive to receive notifications
across turns. Everything else (build, bind-mount, secrets) is comparatively
mechanical.

## Decision

**Ship a `Dockerfile` + entrypoint that runs a persistent, interactive Claude
Code session inside the container**, joined to a channel via the host's
bind-mounted channel directory (`AGENT_CHAT_ROOT`). The session runs in **tmux**
(a pty the unattended TUI needs) under `--dangerously-skip-permissions`, seeded
with a first-turn prompt to join + make its Monitor call + idle. Five design
calls fall out:

1. **Interactive session, not headless `claude -p`.** Headless exits after one
   turn and can't hold a Monitor subscription. tmux supplies the pty so the TUI
   runs detached and idles between dispatched tasks.

2. **Pre-accept the first-run gates in config.** `--dangerously-skip-permissions`
   does *not* cover Claude Code's onboarding, per-folder trust dialog, or the
   bypass-mode acceptance prompt. The entrypoint pre-writes `~/.claude.json`
   (`hasCompletedOnboarding`, `projects[ws].hasTrustDialogAccepted`) and
   `~/.claude/settings.json` (`skipDangerousModePermissionPrompt`) so boot is
   fully unattended.

3. **Auth is injected at run time, never baked in.** Precedence:
   `CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`; portable, ~1yr,
   recommended for remote hosts) > `ANTHROPIC_API_KEY` > a mounted subscription
   creds blob (copied to a writable path so the token can refresh). `docker
   history` shows no secret. A GitHub PAT (`GITHUB_TOKEN`) is wired into git's
   credential helper for private clone/push.

4. **The task toolchain is baked in, tracking the workload.** The worker runs as
   non-root `node` (uid 1000) with no sudo, so on-task `apt` is a dead end —
   `go`, `make`, `python3`, `curl`, `git` are baked in. The Go version tracks
   the **consumer's** requirement (the target workload pins `go 1.25.9`),
   overriding the repo's usual previous-major default, with `GOTOOLCHAIN=auto`
   as a drift fallback.

5. **No repo is baked in; `/workspace` starts empty.** Code arrives either by
   bind-mounting a host repo (`--workspace`) or by clone-on-task via the PAT.
   Baking a checkout would pin the image to one repo and risk staleness.

Same-machine only: the container shares the host filesystem via the bind-mounted
channel dir. Cross-host channels remain out of scope (see Consequences).

## Alternatives Considered

**Headless `claude -p` per task.** Spawn a fresh headless session for each
dispatched task. *Pros:* no persistent process; no tmux. *Cons:* can't hold a
Monitor subscription, so it can't be *handed* work on a channel — it would have
to poll, defeating agent-chat's push model; cold-starts every task. Rejected:
it doesn't satisfy the "idle worker that wakes on a notification" requirement.

**Bake credentials / repo into the image.** *Pros:* simplest run command.
*Cons:* secrets in `docker history`; image pinned to one repo and one identity;
rebuild to rotate. Rejected on the secrets AC alone.

**API-key-only auth.** *Pros:* trivially container-friendly. *Cons:* bills
per-token instead of using the subscription; not what the operator wants for a
personal worker. Rejected as the *default*, kept as an option.

**Build the crash-survival supervisor now.** *Pros:* closes the reliability
story in one issue. *Cons:* the persistent-session lifecycle (relaunch, rejoin,
long-idle stream survival) is its own body of work; the dispatch mechanism is
provable without it. Deferred to a follow-up (mostly a `--rm` → `--restart`
run-mode flip, since the entrypoint re-joins on container restart).

## Consequences

- **The dispatch substrate works:** an idle, channel-subscribed worker wakes on
  a dispatched task, executes (including a private clone + real Go build), and
  reports back — proven end-to-end with no external dispatcher in the loop.
- **A worker exits if its session exits** (no supervisor yet). Crash-survival,
  long-idle stream survival, and disk-growth cleanup are tracked as follow-ups.
- **uid/gid is a deployment concern.** On Docker Desktop / OrbStack for Mac the
  bind mount maps through; on native Linux the channel dir must be
  group-writable with a matching `--group-add`. The entrypoint fails loud on an
  unwritable mount rather than dropping messages silently.
- **Cross-host workers need a different transport.** Running a worker on a remote
  host (e.g. a Raspberry Pi) is gated on a network channel transport, since the
  channel is a shared local file. Tracked separately; not relaxed here.
