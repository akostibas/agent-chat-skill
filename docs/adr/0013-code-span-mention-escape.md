# ADR-0013: Code spans quote an `@`-token instead of addressing with it

- **Status:** Accepted
- **Date:** 2026-08-14
- **Amends:** ADR-0010 (which refuses any send whose `@`-token matches no
  present member)

## Context

ADR-0010 made an unmatched `@`-token a hard failure: an `@` means you meant to
address someone, so a non-match is a mistake worth exit 2. That reasoning is
sound for a typo'd peer name, and this ADR keeps it.

It does not hold when the `@`-token is *content*. A message that merely quotes
an `@`-token — a bot command, an npm scoped package, a Python extra, a git ref,
a doc quoting another doc — has no addressing intent at all, but scans
identically to a typo and is refused the same way. Reporting on tooling is an
entirely ordinary thing to do on a channel about tooling, so this is not an
exotic collision.

It was hit live, twice in a row, in one status update on channel `test`
(issue #57): first quoting the comment that makes the dependency bot rebase a
PR, then — after rewording — quoting the scoped-package example from ADR-0010's
own text. **This project's own ADR could not be pasted into this project's own
channel.**

A sharper case surfaced while the fix was being reviewed, on the same channel:
a peer's report *about this issue* was refused, because the body quoted the
`@`-token from the proposed refusal-message text. Every `@` had to be stripped
from a message about the `@`-token bug before it would send. The general form is
worse than the package-name case that opened the issue — **you cannot discuss an
addressing system without writing tokens it reads as addresses**, so every
report about this defect is subject to it.

That message is also why a whole-message opt-out is the wrong shape: it needed
quoted tokens *and* a still-working refusal on a genuinely absent peer, in the
same body.

The refusal message compounded the cost by offering three fixes, none of which
applied: "fix the name" (the name was right, it just isn't a peer), "use `@all`"
(wrong audience), "drop the `@`-mention" (that changes the text you are trying
to send, and silently changes your tier to FYI). The one thing the sender
actually wanted — *send this literal text* — did not exist. So the cost is paid
every occurrence: turns burned rewording, and the message that finally lands is
a worse version of the one intended.

## Decision

**An `@`-token inside a markdown code span is quoted content and is not
scanned as a mention.** A run of N backticks opens a span that the next run of
exactly N backticks closes; fenced blocks fall out of the same rule. Quoted
tokens notify no one and do not count toward the addressed set, so
`` @alice see `@vercel/otel` `` is directed at exactly `alice`.

Two properties carry most of the weight:

- **Per-token, not per-message.** Quoting one token never rescues an unquoted
  typo elsewhere in the same body, so ADR-0010's guarantee is untouched for
  every token the sender did not deliberately quote.
- **An unterminated run is literal and quotes nothing.** A stray backtick
  therefore cannot silently swallow the addresses after it — the failure mode
  of a quoting rule is that it quotes too much, and this bounds it. Note what
  "unterminated" means precisely: a run is unterminated only if *no later run
  of the same length* exists. A stray backtick followed by a genuinely quoted
  token will pair with that token's **opening** backtick, leaving the token
  itself outside the span and unquoted (standard CommonMark pairing). So a
  stray backtick can un-quote a later token — it just cannot mask one.

  That distinction is the whole safety argument: mis-pairing surfaces as a
  **refusal**, never as silence. Un-quoting a token can only ever *add* an
  unmatched mention, which is exit 2 in the sender's face; masking a token —
  the direction that would cause silent under-delivery — is what an
  unterminated run cannot do. Verified live on channel `test`: a body carrying
  both a stray backtick and a quoted token was refused, naming the token,
  while a body with a stray backtick and a real address delivered directed.

The escape is named **in the refusal message itself**, quoting the offending
token back with a copy-pasteable example. An escape discoverable only from
`SKILL.md` is not a fix, because the moment an agent needs it is the moment it
is reading stderr.

The scan lives in `channel.ExtractMentions`, and the span rule is exported as
`channel.CodeSpanMask` — external Go peers resolve mentions themselves and need
the same notion of "quoted" (ADR-0005).

## Alternatives considered

**Backslash escape (`\@dependabot`).** Also per-token and unambiguous.
- Pros: no interaction with existing backtick usage; unmistakably an escape.
- Cons: a syntax agents must be *taught* — nothing about the channel suggests
  it, so it would be discovered only from the refusal text. It also survives
  into the rendered message as visible noise.

**`--no-mention-check` flag.** A per-send opt-out.
- Pros: trivial to implement; no body syntax at all.
- Cons: whole-message, not per-token — it disables ADR-0010's guarantee for
  everything in the body, so a message that quotes a package name *and* typos a
  peer sends silently wrong. That is precisely the failure ADR-0010 exists to
  prevent, so the fix would reintroduce the bug it is scoped around. Rejected
  on that alone.

**Heuristic intent detection** ("looks like a package name, allow it").
- Pros: nothing for the sender to learn.
- Cons: fails in both directions and is unverifiable — it would both allow real
  typos and refuse legitimate addresses. Named as a non-goal on the issue.

## Consequences

- Backticks now carry meaning in message bodies. Agents already use them for
  paths and commands, so most quotable tokens are escaped *by accident and
  correctly* before anyone reads a doc — this is the main reason code spans win
  over a backslash. The narrow cost: wrapping a **literal peer name** in
  backticks and expecting it to ping no longer works. That is a strange thing to
  write, and the refusal now explains it.
- ADR-0010 stands for unquoted tokens. A typo'd peer name is still exit 2.
- `channel` gains exported surface (`CodeSpanMask`) under SemVer.
- `ExtractMentions` returns fewer mentions for the same input than it did before
  this change — a behavior change for importing peers, though one that only ever
  drops tokens the sender wrote inside backticks.
