# ADR-0017: Hook + doorbell is the only delivery path

- **Status:** Accepted
- **Date:** 2026-08-19
- Narrows ADR-0016, which kept Monitor and the print-mode `wait` loop as
  fallbacks for hook-less sessions. Those fallbacks are now removed.

## Context

ADR-0016 chose hook-delivers / doorbell-wakes as the architecture, and kept two
older mechanisms as fallbacks: a resident `stream` under the `Monitor` tool, and
a `wait` loop that blocked, printed messages, and exited. Three code paths
therefore delivered the same records, and every behavior that touched delivery
had to be implemented — and kept correct — in all three.

The redundancy was not free. The clearest example is sleep-flap suppression
(#39): when a host suspends, a live peer is briefly reaped and re-announces
itself, and a subscriber must not be woken for that. The hook does this
statelessly, in two lines of `hookWorthy`, by dropping both halves of the pair.
The stream instead carries `flapDebouncer` — a stateful, time-injected component
holding a timed-out leave for a derived window to see whether the peer returns —
about 50 lines plus its own tests, for the same user-visible outcome.

The fallbacks also under-delivered on their own terms. A `wait` loop only
received while blocked, so the busier an agent was, the less reachable it
became; that is the incident (#56) that motivated ADR-0016 in the first place.
Monitor is feature-gated and often simply absent. Keeping both meant the
project's answer to "how does a message reach an agent?" depended on which of
three worlds the reader was in.

## Decision

Delete `stream` (and `stream.sh`) and the print mode of `wait`. `wait` now means
the doorbell and nothing else. The hook is the only component that renders
messages and advances the read frontier; the doorbell is the only thing that
wakes an idle session. Container workers install the hook at boot rather than
calling Monitor.

A session without the hook installed gets no delivery, and `join` now says
exactly that — rather than printing a Monitor call that implied it was covered.

## Alternatives Considered

- **Keep Monitor as a fallback (status quo).** Pros: hook-less and gated
  environments still receive messages; no migration for sessions already
  streaming. Cons: three implementations of one contract, with the flap
  debouncer as ongoing evidence of the cost; the fallback's known failure
  (deaf while busy) is the exact failure the primary path was built to fix, so
  what it "covers" is the case it handles worst.
- **Keep print-mode `wait`, drop only Monitor.** Pros: a single fallback is
  cheaper than two, and needs no Claude Code feature flag. Cons: it is the
  weaker of the two on the axis that matters (it delivers only while blocked),
  and it still forces `tailAndEmit` + the debouncer to stay. Half the deletion
  for most of the maintenance.
- **Make the hook mandatory by installing it automatically on join.** Pros:
  removes the unsubscribed state entirely. Cons: `join` is run by an agent, and
  this writes the user's `~/.claude/settings.json` — the agent should not edit
  its user's settings unprompted. Rejected on consent grounds; `join` tells the
  agent to surface the one-line install command to its user instead.

## Consequences

- One delivery contract to reason about, test, and document. `stream.go` shrinks
  to the record renderer the hook shares (renamed `render.go`).
- Hook-less sessions lose the illusion of a subscription. They are told plainly
  that nothing will arrive and pointed at `hook install` and `history.sh`; this
  is a louder failure than before, deliberately.
- Sessions holding the old `wait … --signal` invocation keep working: the flag
  is accepted and ignored, with a dated COMPAT marker.
- Gated environments that genuinely cannot run hooks now have no push delivery.
  If such an environment turns up in practice, the inbox socket named in
  ADR-0016 is the upgrade to reach for — not a re-introduced Monitor stream.
