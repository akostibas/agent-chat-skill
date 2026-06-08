# ADR-0003: Announce departures via presence heartbeats, not a teardown trap

- **Status:** Accepted
- **Date:** 2026-06-07
- **Related:** GitHub issue #4

## Context

`stream.sh` is supposed to post a `leave` event when an agent's session ends,
the mirror of the `join` event. The original implementation relied on a shell
trap: `trap 'emit_leave; exit 0' INT TERM HUP`. In practice peers rarely saw a
sign-off.

A telemetry probe (`bin/teardown-probe.sh`) run under the real `Monitor`
primitive settled why. On session close, Claude Code tears the monitored child
down with **`SIGKILL`**: the probe's log stopped mid-heartbeat with no trapped
signal recorded and no `EXIT` trap firing, and the process was not orphaned
(its parent PID never reparented to 1). `SIGKILL` cannot be trapped, so a
mechanism that depends on the *dying* process announcing its own death cannot
work for the normal teardown path. A parent-death poll does not help either —
the child is in the killed process group and dies simultaneously.

The requirement is stronger than the trap can satisfy: a sign-off must appear
*even when the session and its child shell are hard-killed*.

## Decision

**Detect departures externally, from a surviving peer, using presence files on
the shared filesystem.**

- Each streaming agent owns `<channel>/presence/<name>`; its **mtime** is a
  liveness signal. `stream.sh` runs a background heartbeat that re-`touch`es it
  every `HEARTBEAT_SECS` (default 15).
- On each tick the same loop **reaps**: it scans peers' presence files and, for
  any whose mtime is older than `STALE_SECS` (default 45 — three missed beats),
  posts a `leave` on that peer's behalf. `send.sh` reaps too, so a send-only
  agent still helps clear the dead.
- Reaps are claimed under the existing channel lock by **removing the presence
  file before posting** the leave. Concurrent reapers therefore cannot
  double-post: whoever deletes the file first is the one that announces.
- The `INT/TERM/HUP` trap is **kept as a fast graceful path** and now also
  deletes the agent's own presence file, so a clean exit is never re-reaped.
  Exactly one `leave` is posted per departure.

This works because every agent already shares one filesystem. A hard-killed
process can't send a message, but the fact of its death is durably on disk: a
file that stops getting newer. `kill -9` can't keep a file from aging.

## Alternatives considered

### A. Trap-only (status quo)

Catch `INT/TERM/HUP` in the dying process.

- **Pros:** trivial; instant on graceful signals; no new on-disk state.
- **Cons:** the measured teardown is `SIGKILL`, which is un-trappable — so this
  fails on the *common* path, exactly the case the feature exists for.

### B. Trap + parent-death poll

Keep the trap; also poll `kill -0 $PPID` and self-announce when the monitor
parent disappears (covers orphaning).

- **Pros:** catches a lingering-orphan teardown without a peer.
- **Cons:** the probe showed the child is *not* orphaned — it's killed in the
  parent's process group, so the poll dies with it. Solves a case that doesn't
  occur and still misses the `SIGKILL` case.

### C. Presence heartbeat + peer reaping (chosen)

See Decision.

- **Pros:** robust to any death of the peer, including `SIGKILL` and an
  orphaned-then-killed group; no daemon, no IPC, no network — just mtimes;
  heartbeats are silent (filesystem touches), so they add zero channel traffic.
- **Cons:** sign-off is delayed by up to `STALE_SECS` rather than instant; adds
  a `presence/` subdir to the channel layout; requires at least one live peer to
  do the reaping (see Consequences).

## Consequences

**Positive:**
- A departure is announced regardless of how the session died.
- No extra channel notifications from liveness: the heartbeat only writes to the
  log when it actually reaps a vanished peer (one line per departure).
- Mechanism is observable and testable from a shell — the smoke test drives
  reaping deterministically by backdating a presence file.

**Negative / accepted:**
- **Latency.** Hard-kill sign-offs lag by up to `STALE_SECS`. Tunable via
  `AGENT_CHAT_STALE_SECS` / `AGENT_CHAT_HEARTBEAT_SECS`.
- **Residual gap (inherent).** If an agent is `SIGKILL`ed while *no* peer is
  streaming, nobody is present to reap it; the stale presence file is reaped by
  the next agent to join and stream. If no one is listening, there is nothing to
  announce and no one to announce it.
- **Layout change.** The new `presence/` subdir is additive and
  backward-compatible: an old agent simply doesn't heartbeat, and because it
  writes no presence file it is never reaped — it degrades to the prior
  (no-sign-off) behavior rather than misfiring.
