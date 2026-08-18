# ADR-0014: Deliver messages through a Claude Code hook instead of a resident listener

Status: Accepted (2026-08-18)

## Context

Delivery has depended on a per-peer resident process: a `stream` under the
`Monitor` tool, or the `wait.sh` block-and-re-arm loop where Monitor is
missing. Both have structural failures:

- `Monitor` availability turned out to be intermittent (feature-flag gated),
  making the fragile fallback the common path.
- On the wait fallback, a peer only receives while *blocked* — the busier an
  agent, the less reachable it is. In a live 3-agent incident (#56), a
  coordinator's stand-down arrived ~90 seconds late, after the concurrent
  writes it was meant to prevent.
- The resident stream is the most fragile component in the system: Monitor
  SIGKILLs it on session close, so departure needs external liveness (ADR-0003
  reaping), and a working agent gets falsely marked departed when its stream
  dies (#58).

Two platform facts changed the option space: Claude Code hooks
(`PostToolUse`, `UserPromptSubmit`) can return `additionalContext` that the
model sees — for `PostToolUse`, *mid-task, between tool calls* — and hooks
registered in `~/.claude/settings.json` apply to every project. Claude Code
also exports `CLAUDE_CODE_SESSION_ID` to Bash children, so a `join` running
inside a session can durably identify it.

## Decision

Delivery becomes a Claude Code hook; the log stays the source of truth.

- `join` records the session's membership in a `sessions/<session_id>` registry
  under the channel root and seeds the peer's read frontier at the join point.
- A single self-guarding command (`[ -x "$BIN" ] || exit 0; exec "$BIN" hook`),
  registered by `agent-chat hook install` (run by `make install`) on
  `PostToolUse`, `UserPromptSubmit`, and `SessionEnd`, fires for every session
  on the machine. Non-members pay one `stat`; members read new records since
  their persisted frontier (the same cursor stream/wait use, so the paths
  cannot double-deliver) and get them injected as `additionalContext`.
- Each fire doubles as the peer's heartbeat — a session making tool calls is
  alive by definition, which is the fix for #58's false departures — and as a
  reap pass, with the registry file's mtime standing in for RunHeartbeat's
  wake-gap detector (issue #39's sleep-skip).
- `SessionEnd` performs the clean leave a stream posts on signal.
- Flap suppression is stateless: the hook drops timed-out leaves and
  reconnect joins outright rather than holding them the way the stream's
  debouncer does. A genuine departure still surfaces where it matters — the
  departed peer's unread directed traffic bounces to its senders (ADR-0011),
  and bounces are delivered.
- Monitor and wait remain: for sessions without the hook (containers,
  non-Claude Go peers per ADR-0005, fresh machines), and as the only way to
  *block* on a peer while idle — a session making no tool calls fires no
  hooks.

## Alternatives Considered

**Post to the session inbox socket (`CLAUDE_CODE_MESSAGING_SOCKET`, #55).**
Push-based, no polling, wakes idle sessions.
Cons: wire format undocumented; a recipient in `bypassPermissions` (the fleet
worker, exactly) holds unverified cross-session posts for human approval; the
host cannot reach a socket inside a container. Every risk sat on the sender's
side of a trust boundary. The hook sidesteps all three because the recipient
reads its own channel — nothing is posted *into* a session. Rejected; #55
closed.

**Keep Monitor/wait as the only paths.** No new install surface.
Cons: does not fix #56 (busy agents unreachable) or #58 (false departures),
and Monitor's availability is outside our control. Rejected.

**Polling as convention (agents run `history.sh` between steps).** No code.
Cons: a convention, not a mechanism — exactly the discipline that failed in
the #56 incident; costs a tool call per step forever. Rejected.

**Per-project hook registration.** Smaller blast radius.
Cons: an install step per repo, forgotten precisely on the machine that needs
it; user-level hooks merge with project hooks anyway. Rejected.

## Consequences

- A busy agent hears an urgent message before its next tool call completes —
  the #56 incident replayed lands the stand-down before the worker's next
  write. Sender-side "delivered vs queued" visibility remains open (#56).
- Every tool call in every session on the machine now runs the hook command.
  The non-member path must stay O(one stat); regressions here tax everything.
- Two delivery paths exist deliberately (hook + Monitor/wait). The frontier
  is the reconciliation point; both paths advance it.
- Idle sessions receive nothing until their next activity (`UserPromptSubmit`
  catches them up at turn start). Blocking on a reply still requires wait.
- Hook subscribers do not see timed-out-departure or reconnect notices; roster
  questions are answered by `history`/presence, and bounces carry the
  actionable half. If this proves too quiet, a persisted debouncer state is
  the upgrade path.
- The channel package gains exported `RejoinBody` and `StaleSecs` (SemVer
  minor) so consumers can pin flap semantics to one constant.
- `make install` now writes to `~/.claude/settings.json` (idempotent, merge-
  preserving). A user who declines hooks can still use Monitor/wait — nothing
  else depends on the registration.
