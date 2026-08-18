# ADR-0015: Idle wake via a signal-only doorbell process, self-healed by the delivery hook

Status: Accepted (2026-08-18)

## Context

ADR-0014 made a Claude Code hook the delivery path: messages inject into an
agent's context between tool calls. That left the stated non-goal of waking a
fully **idle** session (#60), and live testing sharpened it into three
observed failures within minutes: an idle hook subscriber was reaped as
departed after 45s (its presence only beat on tool calls), a broadcast sent
during its reap window was silently lost (the reap had cleared its read
frontier, and resurrection reseeded at the log end), and an armed print-mode
`wait` double-delivered anything the hook consumed (its in-memory cursor
never re-read the shared frontier).

A survey of harness affordances (docs plus this harness's actual tool
schemas) found exactly one ungated primitive that starts a turn in an idle
session from the outside: **a background process exiting**. The inbox socket
is documented but feature-gated (absent on the dev machine), holds
unverified posts behind approval for bypass-mode fleet workers, and can't
cross into containers. In-session cron enqueues a visible turn per fire —
tolerable rarely, annoying as a delivery path. FileChanged hooks are
side-effect-only and cannot start turns or inject context. A Gemini design
consult independently converged on kernel flocks for liveness and confirmed
the busy-agent grace-check; its other suggestions (kqueue, flock-based
presence) were rejected as complexity or protocol breaks.

## Decision

`wait --signal`: a background doorbell process whose only outputs are its
exit and a one-line self-description.

- **Signal, not delivery.** It watches the log for wake-worthy records past
  the peer's persisted frontier and exits empty. The agent's next tool call
  (the mandated re-arm is one) triggers hook delivery. The doorbell never
  prints messages and never writes the frontier — the hook remains the single
  deliverer and single frontier writer, making exactly-once structural.
- **Grace re-check.** On spotting traffic it pauses (~2.5s) and re-reads the
  frontier; if the hook consumed the records — the agent was busy — it keeps
  blocking. Only genuinely idle agents wake. The same re-check absorbs the
  re-arm race.
- **Presence keeper, refresh-only.** While armed it refreshes the peer's
  heartbeat (never creates — a doorbell must not resurrect a peer that left)
  and carries the reap duty with the wall-clock wake-skip of issue #39. It
  retires itself when its peer's presence vanishes.
- **Self-healing via flock + hook, no watchdog cron.** The doorbell holds an
  exclusive flock on a per-peer lockfile; the kernel drops the lock the
  instant the process dies, however it dies. The delivery hook probes the
  lock each fire and injects a one-line re-arm reminder when it finds a dead
  doorbell — the reminder arrives precisely when the agent is next active,
  which is the only time it could act anyway. A lockfile that never existed
  means the agent never opted in: no nag. Deleting it opts out.
- **Frontier mirror.** The session registry mirrors the peer's read offset on
  every hook fire; a resurrect that finds its cursor reap-cleared resumes
  from the mirror, so reap-window traffic is delivered late instead of lost.

## Alternatives Considered

**Inbox socket (`CLAUDE_CODE_MESSAGING_SOCKET`).** True push, wakes idle
sessions. Cons: feature-gated (absent on the reference machine), inbound
holds for bypass-mode workers, container-unreachable — three availability
cliffs on the critical path. Remains the someday upgrade if it ungates.

**Cron as doorbell.** Fires while idle, ungated. Cons: every fire is a
visible turn and tokens even when quiet; latency = period. Rejected as the
mechanism; demoted to optional belt-and-braces, likely unnecessary.

**Cron as watchdog for the wait.** The pre-Gemini design. Cons: up to a full
period of deafness after a doorbell death, plus permanent periodic noise.
The flock+hook probe heals on next activity at zero quiet-cost. Rejected.

**FileChanged hook on an inbox file.** Cons: cannot start a turn or inject
context; side effects only; idle firing undocumented. Rejected.

**kqueue instead of 100ms polling; flock-based presence.** Real but
disproportionate: polling cost is imperceptible, and flock presence is an
on-disk protocol break (SemVer major, external Go peers, container bind
mounts). Rejected.

## Consequences

- An idle subscriber wakes seconds after traffic lands; a quiet channel
  costs it nothing visible — no turns, no tokens, no notifications.
- Forgetting to re-arm degrades to hook-only delivery until the next
  activity, when the nag restores the doorbell; messages meanwhile are late,
  never lost (mirror). No agent memory is load-bearing.
- Idle-but-open sessions stay present and addressable; deliberate leave
  retires the doorbell rather than fighting it.
- One more background process per subscribed channel per session, and a
  `doorbells/` lockfile dir under the channel root (swept when stale).
- Print-mode `wait` remains for hook-less sessions; its known double-delivery
  overlap with the hook is now avoidable by using signal mode, and is
  documented rather than fixed for the legacy path.
