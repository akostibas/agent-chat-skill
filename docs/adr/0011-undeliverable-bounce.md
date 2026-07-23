# ADR-0011: Bounce a departed peer's unread directed messages

- **Status:** Accepted
- **Date:** 2026-07-23

## Context

ADR-0010 made every send name a **reachable** audience, refused at the CLI
otherwise. That check fires at send time — the instant the message is written,
the addressee was a present member. It says nothing about what happens *after*.

If the recipient's session dies (or is closed without a clean leave) between the
send and the moment it would have read the message, the message sits in the log
unread. Presence eventually reaps the vanished peer and posts its `[leave]`, but
the original sender is never told its message went nowhere. A coordinator that
dispatched a task to a worker which crashed seconds later waits on a reply that
can never come — the exact silent-failure mode the product intent (principle 2:
*a message that silently goes nowhere is worse than one that fails loudly*) says
to avoid. This gap was surfaced independently by two models reasoning from the
intent doc alone (the 2026-07-23 brainstorm).

Two facts about the existing system make a clean fix possible:

- **The reap site is already wake-guarded.** `ReapStale` is only invoked from
  `heartbeatTick`, which skips reaping on a wake tick (the clock-jump signature
  of a host resuming from sleep — ADR-0009 / issue #39). So a reap that reaches
  the point of posting a `[leave]` has already been judged genuine, not a sleep
  flap.
- **You can only address a present member** (ADR-0010). So any message that
  names a since-departed peer was sent while that peer was live — its membership
  window bounds what could bounce.

The missing ingredient is knowing *which* messages the departed peer had already
received. The stream held its read position only in memory, so it died with the
process.

## Decision

**When a peer is reaped with directed messages it never read, post a `bounce` to
each such message's sender, in the same locked section as the peer's `[leave]`.**

- **Persisted read frontier.** A streaming peer records its read position (a byte
  offset in the log) to `cursors/<name>` as it delivers — seeded at its start
  point and advanced past each consumed batch. This is a new best-effort on-disk
  artifact; a peer that never records one is treated as having read nothing (all
  its directed messages bounce), which is the safe default. `SaveReadOffset` /
  `ClearReadOffset` are added to the supported package surface so an external Go
  peer can participate.
- **What bounces.** On reap, everything past the departed peer's frontier that
  is a `msg` naming that peer in its mentions. A **broadcast** (`@all`, empty
  mentions) is fire-and-forget and does **not** bounce — the sender did not
  single out the departed peer.
- **Where it goes.** A new `bounce` record kind, `Sender` = the departed peer (as
  `[leave]` already speaks on the peer's behalf), `Mentions` = the one original
  sender, `Body` = a notice echoing enough of the original to identify it. The
  stream narrows a `bounce` by mention exactly like a `msg`, so it reaches only
  that sender.
- **No storms, no cascades.** A bounce is skipped when its own recipient is no
  longer a member (nowhere to land), and a bounce is never generated for a
  non-`msg` record — so a departed sender cannot trigger a bounce into the void
  and bounces cannot bounce.

## Alternatives considered

### A. Delivery/response acknowledgements with a timeout (chosen against, for now)

Have the recipient ack each message; a sender that gets no ack within a window
sees an explicit failure. This also catches a **hung-but-present** peer (still
heartbeating, but its context is wedged) — which the chosen design does not.
**Pros:** strictly more coverage; catches silent hangs, not just deaths.
**Cons:** a heavier protocol (ack frames, per-message sender-side timeout state)
for a failure mode we have not yet observed hurting a real run. Deferred as a
separate story; the bounce is the cheap first step that needs no new round-trip.

### B. Reader-side flap-debounce of bounces

Hold each bounce the way `tailAndEmit` holds a timed-out `[leave]`, releasing it
only if the departure outlives the reconnect window.
**Pros:** a bounce could never survive a flap, even one that slips the source
guard.
**Cons:** meaningful reader-side machinery (holding attributed, sender-filtered
records in lockstep with a leave) to close a gap the source-side wake guard
already closes in the common case. Rejected as disproportionate for the MVP; see
the accepted edge below.

### C. Post the bounce as an ordinary `msg` from a system pseudo-sender

Reuse `msg` instead of a new kind so the existing mention filter narrows it for
free.
**Pros:** no filter change.
**Cons:** a bounce would be indistinguishable from a peer speaking, and there is
no established system pseudo-sender (reaps already attribute to the subject
peer). A distinct `bounce` kind is self-describing in history and in SKILL.md,
and narrowing it took a one-clause filter change.

## Consequences

- **Positive:** a sender whose addressed message outlived its recipient learns so
  — the dispatch-and-wait deadlock becomes a loud, immediate notice. The read
  frontier this introduces is reusable groundwork (e.g. resume-from-offset).
- **Accepted edge — flap.** Bounces ride the source-side wake guard but are not
  *also* reader-side flap-debounced (alternative B). A reap that slips the guard
  — rare, since post-#39 a stale presence on a normal-gap tick means the stream
  process is genuinely gone — would emit a spurious bounce. Judged acceptable;
  revisit if it bites.
- **Scope — directed only.** Undelivered broadcasts never bounce. Intentional:
  the value is for "I asked *you* specifically," not fire-and-forget.
- **On-disk / package surface.** Adds a `cursors/` subdir per channel and a
  `bounce` record kind. `Record`'s JSON schema is unchanged (no new field; `kind`
  is an existing string), so the golden schema holds. Per the versioning policy a
  new kind + new exported methods are a **minor** bump pre-1.0.
- **Non-goal intact.** No acknowledgement or read-receipt protocol is introduced;
  the frontier is a local best-effort hint, not a delivery guarantee.
