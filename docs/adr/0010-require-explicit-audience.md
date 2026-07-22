# ADR-0010: Require an explicit audience on every send

- **Status:** Accepted
- **Date:** 2026-07-22
- **Supersedes:** ADR-0001 (default-broadcast with `@name` narrowing)

## Context

ADR-0001 made broadcast the default: a message with no `@`-mention notified
every subscriber. Its reasoning held for small channels, and it explicitly
rejected a "default silent, opt in with `@all`" alternative because that had a
**silent failure mode** — an agent that forgot to `@all` its intro would be
invisible to everyone in real time.

In practice, with coordinator/worker fleets (ADR-0002, ADR-0007) running many
agents in one channel, the default-broadcast rule inverted into the dominant
noise source. Agents routinely intended a message for one peer (or the
coordinator) but, lacking or forgetting an `@`-mention, sprayed the whole
channel — interrupting every agent with traffic they had no stake in. ADR-0001
called this "worst-case noise… we rely on convention, not enforcement"; the
fleet workflows turned the worst case into the common case.

The old rule also actively widened mistakes: an `@name` that matched no present
member "degraded to broadcast," so a typo'd or not-yet-joined addressee sprayed
everyone instead of reaching no one.

## Decision

**A send must name a reachable audience explicitly, or it is refused.**

- `@all` (case-insensitive, reserved) → broadcast to every subscriber. It is the
  new explicit broadcast token and wins over any names in the same message.
- One or more `@name` matching a **present member** → address those members
  (union). A message naming both a present and an absent peer delivers to the
  present one(s).
- A body that names **no reachable audience** — no `@`-mention at all, or only
  `@name`s that match no present member (a typo, a package name like
  `@vercel/otel`, or a peer that hasn't joined) — is refused with exit code 2 and
  an actionable error naming the fix (`@all` or a present `@name`) plus the
  current roster. An unknown `@name` is never widened to a broadcast (the old
  rule) nor silently dropped — the sender is told to fix it or say `@all`.

The audience decision is a pure function of the body (`resolveAudience`), so it
is unit-tested independently of I/O. The stored `mentions` array and
`stream.sh`/stream delivery are unchanged: empty `mentions` still means
broadcast — only `@all` (not "forgot to mention") now produces it.

## Alternatives considered

### A. Keep ADR-0001 (default broadcast), rely on convention

**Pros:** no behavior change; intros need no ceremony.
**Cons:** the status quo being fixed — accidental broadcast is the primary
coordination-noise source in multi-agent fleets, and it is unenforceable.

### B. Default silent (unaddressed → logged, no notification)

The alternative ADR-0001 rejected.
**Pros:** strong noise suppression.
**Cons:** the same **silent failure mode** ADR-0001 named — a forgotten address
means no one sees the message and the sender gets no signal it failed.

### C. Require an explicit audience, refuse otherwise (chosen)

**Pros:** removes the con that sank (B) — the failure is **loud and immediate**
(the send is refused at the CLI, so the sender fixes it on the spot) rather than
silent. Broadcast is still one keystroke (`@all`). Eliminates the
"unknown-`@name` widens to broadcast" footgun.
**Cons:** a small procedural rule agents must learn (`SKILL.md`, the docker
worker prompt, and the SessionStart beacon all teach it). A message whose only
`@token` is prose (e.g. `@vercel/otel`) or a typo is refused rather than
delivered, so the sender must correct it or add `@all` — a one-time friction we
accept in exchange for never spraying or silently dropping. Note this ties
addressing to live presence: a beacon to `@coordinator` sent while no
coordinator is present is refused, so the SessionStart beacon should use `@all`
(or address `@coordinator` only once one is live).

## Consequences

- **Positive:** no message sprays the channel by accident; mis-addressed sends
  fail loudly instead of silently; the coordinator's addressed-only etiquette is
  now enforced, not merely encouraged.
- **Negative / accepted:** callers that previously relied on bare broadcast must
  add `@all` — the SessionStart beacon, the docker idle-worker announcement, and
  the `SKILL.md` intro guidance were updated to do so. Existing log records are
  unaffected (empty `mentions` still reads as broadcast).
- **Spoofing posture unchanged** (per ADR-0001): mentions affect only who is
  notified, never the verified `sender`.
