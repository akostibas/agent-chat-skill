# ADR-0007: Coordinator-spawned containerized worker fleet

- **Status:** Accepted
- **Date:** 2026-06-22
- **Related:** issue #17 (workers stall in Auto Mode), [[ADR-0006]] (the
  containerized worker), [[ADR-0002]] (coordinator/worker formation).

## Context

Issue #17 started from a real failure: a worker driving agent-chat **stalls when
its Claude Code session isn't in auto mode** — it stops to ask a human who isn't
watching. The proposed fix was to *detect* the mode from context and adapt. That
investigation came back empty: no `<system-reminder>`, env var, or marker
reliably distinguishes auto mode in this build, so behavior can't be gated on it.
(The "`## Auto Mode Active` reminder" assumed by [[ADR-0002]] does not actually
arrive; `SKILL.md`'s worker guidance was corrected to stop claiming a detectable
in-context signal. [[ADR-0002]] stays as its point-in-time record.)

Meanwhile [[ADR-0006]] gave us a containerized worker that runs
`--dangerously-skip-permissions` — **unattended by construction**. That reframes
#17: rather than teach a human-attended session to detect when it's safe to run
alone, dispatch hands-on work to containers that have no human and no modes at
all. What was missing was the layer above a single container: a coordinator that
sizes a fleet to the task, spawns N workers, drives them over a channel, and
tears them down — without a human launching each session by hand.

Two things had to be settled: **where each worker's checkout comes from / how its
work integrates back**, and **how a worker is prevented from ever blocking on
input**.

## Decision

Add a coordinator-driven fleet layer over the existing per-container primitive
(`bin/docker-worker.sh`), plus the guardrails that make a worker safe to run with
no human present.

1. **`bin/spawn-fleet.sh -n N` fans out over `docker-worker.sh`.** It mints a
   random fleet id, creates a **private, ephemeral channel root** (a temp dir —
   explicitly *not* the user's global `~/.claude/agent-chat`), and launches N
   hardened containers on it, each labeled `agent-chat-fleet=<id>` and given a
   unique container name. It prints the command for the coordinator to join that
   same channel and the teardown command.

2. **Workers clone from GitHub and push branches back.** Each worker clones the
   target repo fresh into `/workspace` at boot (`AGENT_CHAT_CLONE_REPO`), works
   on a branch the coordinator names, and `git push`es it; the coordinator merges
   or opens PRs from the host. No host worktrees, no shared `.git` across the
   container boundary — integration is the ordinary remote-branch flow.

3. **Interactive tools are disabled by default in the image.** The entrypoint
   passes `--disallowed-tools "AskUserQuestion ExitPlanMode"` to every worker
   session. A container can never answer an interactive prompt, so these can only
   hang it. It's enforcement (a disallow list), not a prompt instruction, and a
   default rather than opt-in because *every* containerized worker is unattended.
   Overridable via `AGENT_CHAT_DISALLOWED_TOOLS` (empty opts out).

4. **Teardown is label-scoped.** `bin/teardown-fleet.sh <id>` removes containers
   by the `agent-chat-fleet=<id>` label (never by name parsing) and deletes the
   ephemeral channel root; it deliberately does **not** delete pushed branches,
   which may hold unmerged work.

5. **The coordinator offers, sized to the work.** `SKILL.md` directs the
   coordinator to propose a fleet only for genuinely independent subtasks, give
   each worker a complete self-contained spec (workers can't ask follow-ups), and
   supervise event-driven as in [[ADR-0002]].

This supersedes, for containerized workers, [[ADR-0002]]'s "workers confirm auto
mode at join": a container has no mode to confirm.

## Alternatives Considered

### Workers clone from GitHub, push branches back (chosen)
- **Pros:** Self-contained checkout that mounts cleanly; integration is the
  normal PR/merge flow; teardown is just `docker rm` + remove the temp channel
  (no host state to reconcile). Works for private repos via the injected token.
- **Cons:** A network clone per worker; requires a reachable GitHub remote (no
  purely-local-repo fleets).

### Host git worktree, mount repo + tree into the container
- **Pros:** No network clone; work lands on host branches directly.
- **Cons:** A worktree's `.git` is a file pointing at the main repo's
  `.git/worktrees/<n>` by absolute path, which doesn't resolve inside the
  container unless the main `.git` is also mounted at an identical path —
  fragile and surprising. Rejected: the mount/gitlink coupling isn't worth it.

### `git clone --local` per worker, mount the clone
- **Pros:** Self-contained like the GitHub option but no network; cheap via
  hardlinks.
- **Cons:** Integration is awkward (fetch from each clone, or still push to a
  remote); more host-side bookkeeping for no real gain once a remote exists.
  Reasonable fallback if offline fleets ever matter.

### Detect auto mode and adapt (the original #17 plan)
- **Pros:** Would let an ordinary human-attended session run unattended safely.
- **Cons:** No reliable in-context signal exists in this build (the #17 finding),
  so it can't be built today. Containers make the question moot for workers.
- **Disposition:** Not pursued for workers; revisit only if the harness exposes a
  real mode signal.

### Disallow interactive tools opt-in (per-fleet) rather than as an image default
- **Pros:** Zero behavior change for existing image consumers.
- **Cons:** Leaves the footgun armed for every other unattended worker (e.g.
  Shannon's), which gains nothing from being able to call a tool that can only
  hang it. Rejected in favor of a safe default; documented as a behavior change
  on version bump.

## Consequences

- **#17's core failure is resolved for hands-on work**: dispatch to containers
  that can't stall, instead of detecting a mode we can't see.
- **Existing single-worker / compose consumers are unaffected** except for the
  new default-disallowed interactive tools, which they pick up on a deliberate
  image rebuild and which only removes tools that could already only hang them.
- **The fleet is convention-plus-tooling, not a scheduler.** Spawn is sequential,
  there's no crash supervisor (inherited from [[ADR-0006]]), and the coordinator
  session staying alive is still load-bearing (same caveat as [[ADR-0002]]).
- **Pushed branches outlive teardown by design** — cleanup of merged/abandoned
  `fleet/*` branches is the operator's call, and teardown reminds them.
- Fixed a latent `docker-worker.sh` bug found while building this: its EXIT trap
  used a `&&` chain that returned 1 on the token/`--api-key` auth path, making
  the launcher report failure despite a healthy start.
