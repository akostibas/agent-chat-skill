# ADR-0016: The Claude Code delivery architecture — hook + doorbell

Status: Accepted (2026-08-18). Consolidates ADR-0014 (hook delivery) and
ADR-0015 (idle doorbell) into the single record of how a Claude Code session
receives channel messages, and why neither Monitor nor cron is involved.

## Context

Delivery originally required a resident listener per peer: a `stream` under
the `Monitor` tool, or the blocking `wait.sh` loop. Monitor turned out to be
feature-gated and intermittently absent; the wait loop only received while
blocked, so the busier an agent was, the less reachable it became — a live
incident (#56) put a stand-down message ~90 seconds too late. What the
harness does reliably provide, ungated: hooks that run on every tool call and
can inject context mid-task, and background processes whose exit — for any
reason — wakes the session.

## Decision

Split delivery from wake. The **hook** is the only component that renders
messages and advances the read frontier (exactly-once by construction); the
**doorbell** is a contentless background process whose only job is to exit
when there is something worth waking for, carrying the presence heartbeat
while it blocks.

    sender session                        recipient session
    ┌───────────┐                 ┌───────────────────────────────────────┐
    │  send.sh  │                 │ BUSY: tool call ends                  │
    └─────┬─────┘                 │   └► PostToolUse hook: read frontier, │
          │ append                │      inject new msgs, advance ────────┼──┐
          ▼                       │                                       │  │
    ┌──────────────────┐  read    │ IDLE: wait --signal (bg process)      │  │
    │ channel log      │◄─────────┤   blocks on log, holds flock,         │  │
    │ (append-only     │          │   heartbeats presence                 │  │
    │  JSONL)          │◄─────────┤   traffic? grace-check frontier:      │  │
    │ + read frontier ◄┼──────────┼─┐   consumed → keep blocking          │  │
    └──────────────────┘  advance │ │   untouched → EXIT (empty) ─────────┼─►│ harness
                        ▲         │ │                                     │  │ wakes
                        └─────────┼─┘ next tool call → hook delivers ◄────┼──┘ agent
                                  └───────────────────────────────────────┘

Self-healing closes the loop with no timers: the doorbell's death releases
its kernel flock instantly; the hook probes that lock on every fire and
injects a one-line re-arm nag when it finds a dead doorbell — arriving
exactly when the agent is next active, the only moment it could re-arm. A
doorbell death is itself a wake (process exit), so nearly every failure
self-announces. The registry mirrors the frontier, so any dead window
delivers late rather than losing messages. The unarmed state therefore
persists only through sustained refusal, and costs idle-wake latency, never
delivery.

## Alternatives Considered

- **Monitor (the original design).** A resident stream per peer.
  Pros: true streaming wake. Cons: feature-gated and often absent; SIGKILLed
  on session close so departure needs external reaping; unreachable while
  the agent is busy on the wait fallback. Rejected as anything but a
  legacy fallback for hook-less sessions (ADR-0014).
- **Cron as delivery or watchdog.** In-session cron fires while idle.
  Pros: ungated. Cons: every fire is a visible turn and tokens even when
  the channel is quiet, and as a watchdog it leaves up to a full period of
  deafness. The flock+hook probe heals at zero quiet-cost, so cron adds
  only noise. Rejected (ADR-0015).
- **Inbox socket.** True push, but feature-gated, held-for-approval for
  bypass-mode workers, container-unreachable. The someday upgrade if it
  ungates (ADR-0014/0015).

## Consequences

- Busy agents hear messages between tool calls; idle agents wake in seconds;
  a quiet channel costs nothing visible. Validated live two-directionally
  (~10–15s idle round-trips, 10.5-minute idle with presence held, #60).
- Two components must agree on one contract: the frontier has a single
  writer (the hook), and the doorbell must stay contentless — changes that
  blur that split reintroduce the double-delivery class of bugs.
- Sessions without the hook (containers, gated environments) fall back to
  Monitor/print-wait and accept their known limitations.
- Sender-side "delivered vs queued" visibility remains open (#56).
