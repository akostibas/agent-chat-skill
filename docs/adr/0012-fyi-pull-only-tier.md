# ADR-0012: A pull-only FYI tier for unaddressed sends

- **Status:** Accepted
- **Date:** 2026-07-23
- **Amends:** ADR-0010 (which refused every unaddressed send)

## Context

ADR-0010 required every send to name a reachable audience, refusing an
unaddressed one. It did this to kill *accidental* silent misdelivery — a send
where you meant to `@someone`, forgot, and it vanished. Its reasoning explicitly
**rejected** a "default silent (unaddressed → logged, no notification)"
alternative for exactly that reason.

A side effect: after ADR-0010 there is **no quiet lane at all**. Every message
that reaches the log wakes someone — `@all` wakes everyone, `@name` wakes the
named. In this system a delivered line *is* an interruption (the stream's stdout
is a Monitor wake event that costs the recipient a turn), so there is no way to
put something on the channel *without* interrupting a peer. In a fleet that
inverts into a noise source: ten workers posting routine "still going, no
blockers" have to `@all` to be on the record, and the coordinator eats ten wakes
for status it would rather glance at on its own schedule. The product intent
names attention as the machine's scarcest resource (principle 3) and calls out
"some things are better pulled."

Two facts now defuse the objection that sank ADR-0010's rejected alternative:

- **The silence becomes deliberate.** Zero `@`-mentions can *mean* "I am
  intentionally not waking anyone," distinct from a forgotten address.
- **Directed failure is already loud.** ADR-0011's bounce means a `@name` message
  to a peer who left comes back to the sender — so the silent-failure case
  ADR-0010 cared about is covered where it matters (directed sends), without
  forcing every note to interrupt.

## Decision

**A send with no `@`-mention at all is accepted as a pull-only FYI.**

- **FYI (no `@`-token whatsoever)** → posted as a new `fyi` record kind. It lives
  in the log like any message — visible to `history` and to a joiner catching up
  — but the live stream **never emits it**, so it is never a wake event. The CLI
  confirms with `posted FYI … pull-only, no peer was notified`.
- **Mis-addressed (`@`-token present, but no present member matches)** → still
  **refused** (ADR-0010 unchanged). An `@`-token signals intent to address
  someone, so a non-matching one is a likely typo, not a quiet note.
- **`@all` and `@name`** → unchanged (broadcast / directed push).

The tier is a pure function of the body's addressing (`resolveAudience` now
returns one of refuse / broadcast / directed / FYI), so it stays unit-testable
without I/O, and the interruption a message costs is the sender's explicit
choice.

| Body addressing | Before (ADR-0010) | After (this ADR) |
|---|---|---|
| `@all …` | broadcast (wake all) | unchanged |
| `@name …` (present) | directed (wake them) | unchanged |
| `@name …` (none present) | refused | refused (unchanged) |
| no `@` at all | refused | **FYI: logged, wakes no one, seen on pull** |

## Alternatives considered

### A. Encode the tier as a new `fyi` record kind (chosen)

**Pros:** minimal footprint — the stream filters one kind, `Record`'s JSON schema
is unchanged (no new field; `kind` is an existing string), and it is
self-describing in history (`[fyi]` reads as "a note nobody was paged for").
Consistent with how ADR-0011 introduced `bounce`.
**Cons:** a consumer that special-cases `msg` must also consider `fyi`; in
practice only the stream and history read kinds, and both were updated.

### B. Make `@all` explicit, let empty-mentions mean FYI

Store `@all` as a reserved marker so an empty `Mentions` slice could mean "no
audience = FYI."
**Pros:** the `Mentions` field alone would fully determine the tier.
**Cons:** changes the on-disk representation of every `@all` message (today an
empty `Mentions`), a larger and riskier format change than adding one new kind,
for no delivery-visible gain.

### C. A dedicated `Record` field (e.g. `Silent bool` / `Tier string`)

**Pros:** models delivery tier as orthogonal to kind.
**Cons:** adds a field to the SemVer-governed schema for a distinction the kind
already carries cleanly. Rejected as heavier than the problem needs.

## Consequences

- **Positive:** the channel gains a quiet lane — routine status and breadcrumbs
  can stay on the record without spending any peer's attention, directly serving
  intent principle 3. Unaddressed sends are legal again, but as *deliberate*
  silence rather than accidental broadcast (the old ADR-0001 behavior) or a hard
  refusal (ADR-0010).
- **Accepted:** an FYI is seen only if a peer pulls it (history / catch-up) — by
  design; it is the wrong tier for anything that needs action, and SKILL.md
  teaches the distinction (direct ask → `@name`; must-see-now → `@all`;
  status/breadcrumb → no address). This leans on non-goal 7 (history is for
  catching up, not an archive).
- **Scope:** this is the *pull* half of attention-tiered delivery (issue #43).
  The complementary *push*-with-opt-in half — subscribable `#topics` that wake
  only current subscribers — is a separate change and will supersede this ADR's
  addressing table when it lands.
- **Wire/versioning:** adds the `fyi` kind. `Record`'s JSON schema is unchanged,
  so the golden schema holds. Per the versioning policy a new kind is a **minor**
  bump pre-1.0.
