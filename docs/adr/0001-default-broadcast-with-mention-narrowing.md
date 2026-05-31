# ADR-0001: Default-broadcast notifications, narrow with `@name`

- **Status:** Accepted
- **Date:** 2026-05-30
- **Related:** GitHub issue #2

## Context

The skill started as a 2-agent point-to-point channel. The README and original
design assumed every message in the channel was meant for the other agent —
`stream.sh` only filtered out the agent's own messages and notified on
everything else. With 2 agents this is correct: every peer message is for me.

With 3+ agents, that model breaks down. Two agents having a focused side
exchange interrupt every other agent in the channel with notifications that
don't concern them. As channels grow past pairs (which the README explicitly
invites), the cost of being subscribed grows linearly with traffic an agent
has no stake in.

We need an addressing model: a way to mark a message as "for someone specific"
vs "for the room." The question this ADR answers is **which direction is the
default** — silent unless addressed, or broadcast unless narrowed.

## Decision

**Messages broadcast to all peers by default. `@name` narrows the audience to
the named peers.**

Concretely:

- A message with no `@`-mentions notifies every subscriber except the sender.
- A message containing `@bob` notifies only `bob` (plus suppression of the
  sender's own notification, as today).
- Multiple `@name` tokens union: `@bob @alice` notifies both.
- `@name` matching is whole-token (the `IDENT_RE` grammar already used by
  `validate_ident`) so `@alice` does not match `@alice-frontend`.
- No `@all` keyword. The default *is* `@all`; a reserved word would be
  mechanically redundant.

Mentions are parsed at send time and stored as a `mentions: ["bob", ...]`
array in the JSONL record. `stream.sh` decides whether to emit a notification
by checking `(mentions | length == 0) or (mentions | contains(me))`.

Join/leave events ignore the mention array and always broadcast — peers need
to know who is in the room regardless of addressing convention.

## Alternatives considered

### A. Default silent, opt in with `@name` / `@all`

The first proposal: unaddressed messages append to the log but produce zero
notifications. Agents would explicitly `@all` for broadcasts (e.g. their
introduction message).

**Pros:**
- Strongest noise suppression — agents only ever ping on deliberate
  addressing.
- Matches the issue's original wording ("only addressed messages interrupt").

**Cons:**
- **Silent failure mode.** An agent that forgets the `@all` convention sends
  an intro that no peer sees in real time. New agents look invisible until
  someone happens to check `history.sh`.
- Adds a procedural rule agents must remember and that we'd have to teach in
  `SKILL.md` ("always `@all` your intro until comms patterns are
  established") — a workaround for a design that defaults the wrong way.
- The noise problem this is solving isn't *broadcasts* — broadcasts are rare
  and deliberate. It's *focused exchanges between two agents in front of
  others*. Those exchanges naturally get addressed (`@bob` because you're
  replying to bob), so default-silent gives no extra benefit over
  default-broadcast for the actual failure mode.

### B. Default broadcast, narrow with `@name` (chosen)

See Decision.

**Pros:**
- **Loud failure mode.** Forgetting to address a message is recoverable
  (peers see it; they can ignore). Forgetting to broadcast under (A) is not
  (the message is invisible to everyone in real time).
- No procedural rules for agents — the natural way to write a reply
  (`@bob, here's what I found…`) is also the correct way to keep noise down.
- Matches how channels in Slack/IRC/Discord actually work: posting in the
  room reaches the room; DMs/threads narrow it.
- Eliminates the need for `@all` as a keyword.

**Cons:**
- Worst-case noise (an agent that never addresses anyone) equals today's
  behavior. We rely on convention, not enforcement, to keep traffic
  addressed.
- Slightly higher cognitive load for the reader: "was this for me, or just
  ambient?" — but every broadcast message is genuinely for the room, so the
  answer is usually "yes, for me."

### C. Per-agent subscription filters (e.g. mute peers, mute regex)

Let each agent configure what they get notified about — by sender, by topic,
by regex.

**Pros:**
- Maximally flexible. Agents could mute a noisy peer or focus on a keyword.

**Cons:**
- Per-agent state to persist somewhere (file? env var?). Adds a config
  surface that didn't exist.
- Pushes the decision to the wrong side: the *sender* knows who a message is
  for; receivers shouldn't have to maintain a model of which senders are
  relevant.
- Overkill for the scale (a handful of agents per channel, ephemeral).
- Doesn't solve the actual problem — an agent who wants to follow the
  channel but isn't in the current side-exchange still gets pinged.

## Consequences

**Positive:**
- Intros and announcements work without ceremony.
- No new etiquette rule for agents to learn.
- Focused exchanges between two agents naturally stay quiet for everyone
  else, because replies are addressed.
- The convention degrades gracefully: a channel of agents that ignore
  addressing entirely behaves exactly like today.

**Negative / accepted:**
- We are not enforcing addressing — we are encouraging it. A misbehaving
  agent (or a confused human) can recreate today's noise problem by
  broadcasting everything.
- `mentions` field becomes part of the log schema. Old records (pre-feature)
  lack the field; `stream.sh` treats absence as broadcast, which matches the
  pre-feature behavior, so no migration is needed.
- Agents wanting to follow a side-exchange they weren't included in still
  need to call `history.sh --since <ts>` to catch up. This is a feature, not
  a regression — the alternative is being pinged on everything.

**Spoofing posture is unchanged.** Mentions are parsed from the sender's
body, but they only affect *who is notified*, not the verified `sender`
field. Per-line `sender` prefix in `stream.sh` still prevents identity
spoofing.
