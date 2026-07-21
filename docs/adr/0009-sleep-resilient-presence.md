# ADR-0009: Sleep-resilient presence — self-healing and wake-aware reaping

- **Status:** Accepted
- **Date:** 2026-07-20
- **Related:** ADR-0003 (presence heartbeat), GitHub issue #29

## Context

Presence is liveness-by-mtime (ADR-0003): each stream touches its own heartbeat
file every `HEARTBEAT_SECS` (15) and reaps any peer whose file is older than
`STALE_SECS` (45), posting a `leave` on its behalf. This assumes wall-clock
continuity between beats. Host sleep breaks that assumption.

When the machine suspends, every stream process freezes together. On resume,
wall-clock time has jumped forward by the sleep duration, so *every* presence
file is now far past `STALE_SECS` — all peers look stale at the same instant.
Whichever peer's heartbeat fires first reaps all the others (a burst of "timed
out" leaves) before they get to refresh. Worse, the routine heartbeat used
`RefreshPresence`, which by design (issue #29) refuses to re-create a deleted
file. So a falsely-reaped-but-still-running agent never returned on its own; the
user had to manually prompt each agent to re-join. Observed symptom: after the
laptop slept, agents "left the chat" and took convincing to come back.

Issue #29's "never resurrect on heartbeat" rule was correct for the bug it
fixed — a *bare* re-create with no `[join]` let a reaper re-announce the same
departure forever. But it over-generalized: it also blocked a live process from
reasserting the one fact it is authoritative about — its own presence.

## Decision

**Make a live stream authoritative for its own presence, and make reaping
wake-aware.** Two changes to the heartbeat loop (`RunHeartbeat`):

1. **Self-healing presence.** Each beat calls `EnsurePresence`: refresh the mtime
   if the file exists, re-create it if it doesn't. When it had to re-create — the
   peer had been reaped — the loop posts a fresh `[join]` carrying the peer's
   identity (cwd/branch). This dodges the #29 re-reap loop precisely because the
   re-create is now paired with a real arrival: flapping shows honest join/leave
   pairs, not bare repeated leaves, and a peer that keeps beating is never reaped
   again anyway. `RunHeartbeat` now takes the identity `Record` (not just a name)
   so the rejoin can carry it. `RefreshPresence` keeps its non-resurrecting
   contract and is still what `send.go` drives.

2. **Wake-aware reaping.** The loop tracks the wall-clock gap between ticks. A gap
   wider than the whole `STALE_SECS` window is never a normal beat — it means the
   clock jumped, i.e. the host resumed from sleep. On such a tick the loop
   refreshes but **skips reaping**, so peers get a beat to refresh before anyone
   is judged gone. The next normal-gap tick reaps as usual, by which point live
   peers have re-touched and only the truly dead remain stale.

Self-heal (1) guarantees correctness — a running terminal is always online, even
if a false reap slips through. Wake-awareness (2) removes the noise — the false
mass-reap mostly never happens, so rejoins are rare.

## Alternatives considered

### A. Self-heal only (no wake-awareness)

Let false reaps happen, rely on self-heal to bring agents back.

- **Pros:** smaller change; correctness is fully restored.
- **Cons:** every wake produces a burst of leave→rejoin churn in the log and in
  peers' notification streams — correct but noisy.

### B. Wake-awareness only (keep the no-resurrect rule)

Skip reaping after a jump, but don't self-heal.

- **Pros:** no API change; prevents the common false reap.
- **Cons:** any false reap that *does* slip through (e.g. a `send`-path reap, or a
  peer whose first post-wake beat lands late) is still permanent — the exact
  "won't come back" failure, just rarer. Doesn't restore the invariant.

### C. Detect wake via an OS signal / power-management hook

Subscribe to a macOS wake notification and pause reaping around it.

- **Pros:** precise wake detection.
- **Cons:** platform-specific, pulls the stdlib-only binary toward cgo/OS APIs,
  and doesn't generalize to any other clock jump (VM pause, NTP step). The
  wall-clock-gap heuristic is portable and catches all of them.

### D. Chosen: self-heal + wake-aware reaping

See Decision. Restores the invariant *and* keeps it quiet, with no new platform
dependency.

## Consequences

**Positive:**
- A terminal still running Claude Code stays online across sleep, with no manual
  re-join.
- Steady state is unchanged and silent: a live peer's beat is a bare mtime
  refresh; `EnsurePresence` only writes a `[join]` when it actually had to
  re-create the file.
- The wake heuristic is portable — it fires for any forward clock jump (sleep, VM
  suspend, NTP step), not just macOS sleep.

**Negative / accepted:**
- **Log churn on a real reap-then-survive.** If an agent is genuinely reaped and
  survives (rare, mostly the sleep case that wake-awareness already suppresses),
  peers see a `leave` then a `reconnected` `[join]`. This is honest, not a bug —
  it is what breaks the #29 loop — but it is two lines where before there were
  zero.
- **API change.** `RunHeartbeat(ctx, name)` became `RunHeartbeat(ctx, Record)`.
  Pre-1.0, so a minor bump (v0.15.0). External Go peers importing the package
  update the call.
- **`send.go` is not wake-aware.** Its one-shot `RefreshPresence` + `ReapStale`
  has no tick history to detect a jump, so a `send` issued immediately after wake
  can still false-reap. Accepted because self-heal makes it non-permanent (the
  reaped peers' own heartbeats bring them back), and sends immediately post-wake
  are rare. If it proves noisy, give `send` a persisted last-activity baseline.
