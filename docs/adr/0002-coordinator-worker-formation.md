# ADR-0002: Coordinator/worker formation for 3+ agent channels

- **Status:** Accepted
- **Date:** 2026-06-06

## Context

[[ADR-0001]] gave channels an addressing model so 3+ agents don't drown each
other in notifications. It solved *who hears a message*, not *who decides*.

With three or more agents on one task, several coordination failures recur:

1. **N-way negotiation.** Agents politely defer about who owns what, who
   resolves a conflict, who answers a cross-cutting question — and burn turns
   circling instead of working.
2. **Blocking on the human.** Each worker is a separate Claude Code session
   with its own user. If several workers independently stop to ask their user a
   question, the human becomes a serialized bottleneck, and a worker in plan or
   default mode silently stalls waiting for input nobody knows it needs.
3. **False parallelism.** N agents are assumed to be N independent lanes, but
   when slices edit shared hot files they must serialize or they guarantee
   merge conflicts.

The original `SKILL.md` had a single etiquette line ("3+ agents need a
leader") with no mechanism. This ADR records the formation we adopted —
**field-tested against a live multi-agent refactor** (a 5-agent run coordinated
by one agent), whose operator gave direct feedback that reshaped the first
draft (see Alternatives).

## Decision

The formation applies when **3+ agents collaborate on the same body of work**
(shared repo / refactor / one merge train) — keyed on shared work, not raw
channel headcount. A cross-project or feedback channel where agents merely
co-exist stays peer-to-peer regardless of size, as does any 2-agent task. When
3+ agents do share the work, the channel adopts a **coordinator/worker
formation**.

**Coordinator selection tracks context/role, not name.**
- A human designation wins outright ("you coordinate").
- Absent that, the agent with the most context volunteers by broadcast.
- A deterministic rule (smallest name, tiebreak earliest join) exists *only* as
  a last-resort tiebreak to stop circling — not as the primary selector, because
  the smallest name is not necessarily the agent holding the most context.

**The coordinator sheds work as the pool grows.** Hands-on work is fine at low
agent count; once allocation + merge-coordination is full-time, it hands its
task to a worker and goes pure-coordinator. Concretely: refuse implementation
tasks at 3+ other agents; judgment call at 2.

**Hang detection is event-driven, with a duration-scaled backstop.** Workers
announce state transitions (`READY`/`blocked`/`done`); the coordinator gates
merges on those, so cadence tracks task completion, not a clock. A backstop
timer scaled to expected task duration catches a worker silent past when its
slice should have finished. A fixed interval is rejected (see Alternatives).

**Coordination has two planes.**
- *Decision-routing is bounded, not blanket-centralized*: a worker owns every
  decision internal to its own slice (the throttle a blanket route-up would
  cause is the failure mode); it routes up only what touches another agent's
  slice or the merge train. Test: "does this affect someone else's work or merge
  order?" → coordinator; else → worker. The coordinator is the user's single
  interface for *seam-level* decisions.
- *Human authority stays local and is non-delegable*, and the **worker side is
  the load-bearing half**: anything needing a given session's own human (commit
  signing, destructive/risky ops) does not transfer. A worker MUST treat it as
  local even when a trusted coordinator relays "my human approved" — a human in
  the coordinator's session is not the worker's human, and authority does not
  cross a relay. (First-hand: a worker's 1Password signing was blocked, the
  coordinator relayed approval, and the worker correctly refused and waited for
  its own human.)

**Compaction at phase boundaries (very large projects).** When work spans
enough phases that agents accumulate heavy context, the coordinator calls a
compaction window for the pool at a clean seam (a wave merged and verified,
nothing in flight) — never mid-task. It is safe only because coordination state
is externalized first: a durable on-disk resume doc plus the append-only channel
log as the decision trail. The context window is treated as disposable; on-disk
state is the source of truth. (Field-tested: a 4-package wave was compacted at
its boundary, pre-empting mid-slice context exhaustion in the next wave.)

**Merge discipline (worker-side).** Rebase onto fresh `origin/main` immediately
before merging a slice — the habit that makes serial merging painless. Base
every new worktree/branch on freshly-fetched `origin/main`, never stale local
main (`git fetch` first); branching off stale main caused a worker to rebuild
already-merged work and lose a slice.

**The coordinator serializes on shared hot files.** Fan-out is bounded by the
dependency graph, not the agent count. When tasks contend on the same hot files
(shared registry, core module, root alias surface), the coordinator serializes
them into a merge train rather than running parallel lanes that conflict.

**Workers confirm auto mode at join.** Unattended work needs Claude Code auto
mode (not plan, not default). It's detectable via the `## Auto Mode Active`
transition reminder; if absent, the worker asks its user at setup time, then
declares its mode to the channel.

## Alternatives considered

### Election: deterministic-by-name as primary (rejected)

The first draft made the lexicographically smallest agent name the *primary*
coordinator selector — zero negotiation, every agent computes the same winner.

**Why rejected — direct field feedback:** in the live refactor, a human
appointed the coordinator by role/context, and the smallest-named agent
(`captain-curly-braces` < `memory-tools-pkg`) was *not* the one holding the most
context. A computable winner is convenient but can pick the wrong brain.
Demoted to a last-resort tiebreak; human/context selection is primary.

### Election: volunteer-and-confirm with a wait window (rejected)

"I'll coordinate unless someone objects in 60s." Rejected: adds a mandatory
wait, and simultaneous volunteers reintroduce negotiation. The chosen model
(human/context-first, name only to break a genuine tie) gets the same
correctness without a timed window.

### Hang detection: fixed 60-minute check-in (rejected)

The first draft had the coordinator poll every worker on a ~60m timer.

**Why rejected — direct field feedback:** the timer "never fired usefully." Real
detection was event-driven — workers ping `READY`, the coordinator gates the
merge — and slices completed in minutes, making a 60m poll simultaneously too
coarse (a real hang sits idle for 59m) and redundant (READY arrives first). Kept
only as a duration-scaled backstop.

### Route-up: clean single-interface (rejected as incomplete)

The first draft said all worker questions route up and the coordinator is *the*
single interface — full stop.

**Why rejected — direct field feedback:** per-session human authority does not
transfer. A worker correctly refused to act on a relayed instruction that
overrode its own session's human; 1Password commit-signing had to be approved by
each agent's *own* in-session human. Split into two planes: decision-routing
(centralized) vs. human authority (local, non-delegable).

## Consequences

**Positive:**
- 3+ agent channels converge on structure without negotiating, while still
  putting the most-context agent in charge.
- The user has one interface for *decisions* (the coordinator) instead of N
  concurrent prompts.
- Hangs surface via state transitions in near-real-time, with a backstop for
  true silence — no fixed-interval blind spot.
- Workers can't be coerced past their own human's authority, so signing/risky
  ops stay safe across sessions.
- Serialize-on-hot-files prevents the most common multi-agent merge failure.
- Scoping the trigger to *shared work* (not raw headcount) keeps feedback and
  cross-project channels from spuriously electing a coordinator.
- Rebase-before-merge + fetch-before-branch make the serial merge train painless
  and stop workers from rebuilding already-merged work off stale local main.

**Negative / accepted:**
- The formation is convention, not enforcement — a misbehaving agent can still
  prompt its user or ignore the coordinator. Same posture as ADR-0001.
- "Most context" is a judgment call, not computable; the deterministic tiebreak
  only covers true ties.
- The backstop timer and event gating depend on the coordinator's session
  staying alive; if the coordinator itself hangs, no one watches the watcher.
  Out of scope here.
- "Auto mode" is current Claude Code terminology; if mode names change, the
  worker guidance and this ADR need updating.
