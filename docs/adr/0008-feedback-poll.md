# ADR-0008: In-channel feedback poll, filed by an approved coordinator

- **Status:** Accepted
- **Date:** 2026-07-11
- **Related:** GitHub issues #31 (parent), #32/#33/#34 (children); [[ADR-0002]]
  (coordinator role), [[ADR-0003]] (presence), [[ADR-0005]] (channel schema)

## Context

Agents are the primary users of agent-chat, and they hit friction we never hear
about — a confusing `join` output, a mention that didn't resolve, a coordination
pattern that fought them. That signal dies at the end of each session. There was
no low-effort channel for the agents themselves to surface "this added friction"
or "here's a process improvement," so the tool improved only when a human
happened to notice a rough edge.

We wanted to harvest that feedback **periodically, cheaply, and rarely** from
live agents and turn it into tracked issues — without spamming duplicates,
without surprising the user, and without assuming every agent environment can
write to GitHub (containerized workers typically can't).

Three questions had to be answered together: *when* a round fires, *where* its
state lives, and *who* is allowed to turn submissions into issues.

## Decision

**Add a `/poll`-style feedback primitive to the `agent-chat` binary, triggered
once per channel by a biased coin flip on the creating join, with all round
state event-sourced in the append-only log and filing gated behind a single
coordinator and explicit user approval.**

1. **Trigger — once per channel, on creation.** The join that *creates* a
   channel (empty log under the lock) rolls a single die
   (`AGENT_CHAT_FEEDBACK_RATE`, default `0.10`, `0` disables). On a hit it opens
   a round; joins into an existing channel inherit the decision and never roll.
   The roll and the `poll-open` record are written under the **same lock** as the
   first join record (`JoinNew`), so two racing first-joiners cannot both open a
   round — only the one writing into an empty log is the creator.

2. **State — event-sourced in the log.** Three additive record kinds
   (`poll-open` / `poll-submit` / `poll-close`), tagged with a new `omitempty`
   `round` field, carry the round's whole lifecycle. Replaying the log
   reconstructs the round; there is no separate mutable state file. The `msg`
   golden schema is unchanged (the field is omitted when empty), so this is an
   additive/minor evolution under [[ADR-0005]].

3. **Filing — one coordinator, human-approved, capability-aware.** Members submit
   items; `tally` produces the deduped candidate list. A single coordinator (the
   opener, or the channel's existing coordinator per [[ADR-0002]]/[[ADR-0007]])
   dedups the list against existing repo issues, **asks its user**, files one
   issue per approved item, and records a terminal outcome
   (`filed` / `declined` / `empty`). If no present member can reach GitHub, the
   coordinator posts the list into the channel for a human and closes the round.

The mechanics (roll, open, collect, tally, dedup-within-channel, close) live in
the binary; the judgment (what's worth reporting, dedup-vs-GitHub, filing) stays
with the agents and their user. The binary never shells out to `gh`.

## Alternatives Considered

**Roll on every join, suppress repeats with a cooldown.**
- *Pros:* stateless per-join; matches a literal "poll agents 10% of the time they
  start up"; no creator-detection needed.
- *Cons:* probability leaks badly. A re-join (stream reconnect, crash recovery)
  re-rolls, and in a multi-agent channel each joiner rolling at 10% compounds —
  10 agents ≈ 1 − 0.9¹⁰ ≈ 65% chance of a round per channel, not 10%. The
  cooldown only papers over missing state. **Rejected** for the once-per-channel
  roll, which gives a true per-channel rate.

**Store round state in a dedicated mutable `state.json`.**
- *Pros:* a literal "session file"; O(1) to read current status.
- *Cons:* a second write path with its own flock, diverging from the channel's
  event-sourced, append-only model; another file for the sweep and for external
  Go peers ([[ADR-0005]]) to understand. **Rejected** — the log already is the
  session state; replaying it is cheap at these sizes.

**Bake `gh` filing into the binary (`feedback file`).**
- *Pros:* one command files everything; no per-agent `gh` toil.
- *Cons:* couples the stdlib-only binary to `gh` auth and to LLM judgment
  (which items match existing issues; per-issue title/body/label). Filing is
  exactly where human/LLM judgment is required. **Rejected** — filing stays in
  the agent's hands via the `story-writer` skill / `gh`, with the binary limited
  to `tally` and `close`.

**Auto-file without a human.**
- *Pros:* zero human toil.
- *Cons:* unreviewed issues are noise; a bad round could spam the tracker.
  **Rejected** — a user always approves; nothing is filed autonomously.

## Consequences

- Feedback surfaces on its own, rarely (~10% of new channels), with no human
  initiative, and lands as tracked issues after a human says yes.
- The channel schema gains three record kinds and one field, all additive; the
  `channel` package gains `JoinNew` (Join now delegates to it) and the
  `FeedbackRound` operations. Minor version bump.
- `join` does one extra `Stat` (log-empty check) under a lock it already holds —
  negligible.
- Because the roll fires only on channel *creation*, a channel that is swept for
  idleness and later recreated under the same slug rolls fresh — acceptable, and
  the only way a long-lived slug ever polls more than once.
- A creating agent that hits the roll but crashes before the round is used simply
  leaves an open round the next joiner inherits; a hit that crashes mid-`JoinNew`
  opens nothing. Both are harmless at a 10% rate.
- Filing depends on a coordinator with `gh` capability being present. The
  no-capability fallback (post to channel for a human) keeps the round from
  dying silently, but delivery then depends on a human reading the channel.
