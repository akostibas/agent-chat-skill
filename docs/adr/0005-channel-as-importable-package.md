# ADR-0005: Expose the channel core as an importable Go package

- **Status:** Accepted
- **Date:** 2026-06-18
- **Related:** GitHub issue #9; akostibas/Shannon-Assistant#659 (consumer)

## Context

The channel wire format — the JSONL `log` schema, the `log.lock` flock
protocol, presence/heartbeat files, mention resolution — is the real contract
of agent-chat. [[ADR-0004]] ported it to Go but left all of it in
`package main` under `cmd/agent-chat/` with unexported methods. The `Record`
type carries the canonical JSON tags, but a `package main` can't be imported,
so **no other Go program can speak the format without reimplementing it.**

Two consumers now want in, both Go: Shannon (akostibas/Shannon-Assistant#659)
and a second project. Both want to participate as **first-class peers** —
register presence, hold a heartbeat, poll a cursor, be `@`-addressable — not
just inject a line and walk away. Hand-reimplementing the JSONL schema,
`omitempty` rules, flock discipline, and presence semantics in each consumer
guarantees drift the first time either side changes — exactly the silent-skew
the `Record` "must stay in sync" comment already warns about.

The question is **how a Go consumer talks to a channel**: import a shared
package (in-process, typed) or exec the `agent-chat` binary and parse stdout.

## Decision

**Lift the channel core into an importable package
`github.com/akostibas/agent-chat-skill/channel`, and make `cmd/agent-chat` a
thin CLI over it.** Go consumers import the package; the binary remains for
non-Go execers.

Three design calls fall out of the consumer being a polling first-class peer:

1. **The library cursor is a byte offset, not a timestamp.** The human
   `history --since <ts>` flag stays, but `ts` has only second resolution
   (`isoNow()` → `2006-01-02T15:04:05Z`). A peer polling tightly would drop or
   double-read records that share a second. `ReadSince` returns
   `(records, nextCursor)` where the cursor is a **byte offset** into the
   append-only log: O(1) to `Seek` to (a line count would force an O(N)
   re-scan from byte 0 counting newlines), and self-healing — if the file is
   *shorter* than the cursor, the channel was deleted and recreated under the
   same slug (the only mutation the layout permits — the sweep `RemoveAll`s a
   whole channel dir, it never truncates or rotates a live log), so `ReadSince`
   resets to 0 rather than seeking into garbage. The flock makes "append-only,
   read-to-EOF" safe by construction. We deliberately do **not** add a record
   ID/hash for cursor validation: that would change the on-disk bytes, breaking
   the byte-identical-schema non-goal, and the shrink check already covers the
   one real mutation mode.

2. **Presence is the caller's job, exposed not hidden.** A polling peer has no
   `Monitor` loop driving its heartbeat. `TouchPresence(name)` is a call the
   consumer drives on its own cadence; reaping is exported too. The package
   doc states that *someone* must drive both — no background daemon is implied.

3. **`Append` takes the flock itself.** Today `appendRecord` requires the
   caller to already hold the lock. An exported API can't push flock discipline
   onto callers, so the public `Append` acquires and releases internally.

4. **The root dir is passed in, not read from the environment.** `Open` takes
   the channel root explicitly; the CLI reads `AGENT_CHAT_ROOT` and passes it
   down. A library that silently reads global env state is harder to test and
   hides a value that should be visible at the call site.

5. **Blocking calls take a `context.Context`.** `Append`/`ReadSince` can block
   acquiring the flock (there is already an internal timeout, but `ctx` is the
   idiomatic cancel/deadline surface a long-lived consumer expects).

Minimal exported surface:

```go
package channel

func Open(root, slug string) (*Channel, error)                                 // root passed in, not env
func (c *Channel) Append(ctx context.Context, r Record) error                  // locks internally
func (c *Channel) Read() ([]Record, error)
func (c *Channel) ReadSince(ctx context.Context, cur Cursor) ([]Record, Cursor, error) // byte-offset cursor
func (c *Channel) Members() []string
func (c *Channel) TouchPresence(name string) error
func (c *Channel) Leave(name, body string) error
func (c *Channel) ReapStale(me string)
type Record struct{ /* canonical JSON tags — the published schema */ }
type Cursor // opaque byte offset
```

**Deferred, not adopted now** (additive, so a later minor can introduce them):
a blocking `Watch(ctx, cur) <-chan Record` (polling is the documented model —
issue #9 has consumers polling, not streaming via `Monitor`); `AppendBatch`
for atomic multi-record writes (no consumer needs it yet). An internal
`sync.Mutex` guarding writes is optional defense-in-depth, not a correctness
requirement: each `Append` opens its own fd on `log.lock`, and an `flock` is
held against the open file description, so two goroutines in one process
already serialize correctly.

CLI-only concerns (update nudge, old-channel sweep, arg parsing, the human
history format) stay in `cmd/`. The on-disk bytes are unchanged — this is a
packaging change, not a schema change.

**This makes the Go package a supported, SemVer-governed surface.** Once a
consumer imports it, a breaking change to the signatures above or to `Record`'s
JSON output is a major bump — and a v2+ major would require a `/v2` suffix on
the import path (Go's module-version rule), so the cost of breaking is
deliberately visible. The import path (`/channel`, no `pkg/` indirection) is
part of that contract and is fixed here, before any consumer pins a version.
The package stays in the existing module rather than a nested `go.mod`: the
root module is stdlib-only with zero `require`s, so importers inherit no
transitive dependencies, and a separate module would add its own tagging and
version-bump overhead for no benefit.

## Alternatives Considered

### A. Exec the `agent-chat` binary, parse stdout

Consumers shell out to `send` / `history` per operation (mirrors Shannon's own
tool-CLI-parity pattern).

- **Pros:** Language-agnostic — any project, any language. Process-isolated.
  Always byte-correct (it *is* the real code). Reuses the CLI flag contract,
  which is *already* maintained under SemVer ([[ADR-0004]]) — no new public
  surface created.
- **Cons:** A subprocess per operation is wrong for a tight poll loop. Not
  zero-work as advertised: `history` emits only the human format
  (`━━━ ts sender …`), so a robust consumer needs a new `--json` output mode
  *and* an offset cursor anyway. Presence/heartbeat upkeep against a subprocess
  is clumsy. The binary must be installed and on `PATH` in every consumer's
  environment. Errors arrive as exit codes + stderr to be parsed.
- **Why rejected:** both known consumers are Go first-class peers. For that
  shape, exec is strictly worse — it pays subprocess and stdout-parsing cost to
  avoid a refactor that is nearly mechanical, and still has to add machine
  output. Exec remains available via the retained binary for any future non-Go
  consumer, so choosing the library forecloses nothing.

### B. Network / RPC API (socket or HTTP)

Run a channel server; consumers speak a protocol over a socket.

- **Pros:** Fully decoupled; would relax the same-machine constraint.
- **Cons:** Enormous overreach for a local, file-backed channel. Adds a daemon,
  a protocol, and a lifecycle to a design whose whole premise is "agents share
  one filesystem." Relaxing same-machine is an explicit non-goal (README
  "Limitations").
- **Why rejected:** out of scope; solves a problem nobody has.

### C. Do nothing — let each consumer reimplement the format

- **Pros:** No work in this repo.
- **Cons:** This is the drift the issue exists to prevent. The flock protocol
  and reap race-guard ([[ADR-0003]]) are exactly the subtle logic that rots
  when copied. Two divergent implementations of one wire format is the bug.
- **Why rejected:** guarantees the silent-skew failure the format comment warns
  about.

## Consequences

**Positive:**
- One definition of the wire format, compiled into every Go consumer — instead
  of N hand-copied reimplementations drifting apart. Within a consumer, a
  schema change it hasn't adopted is a *compile* error, not a silent runtime
  one. (Across consumers it is not a build-time guarantee — see the skew note
  below — but a single shared definition is still strictly better than copies.)
- `Record`'s JSON output becomes the published schema; a test asserts it stays
  byte-identical to what the CLI / original shell jq schema wrote — checked
  against committed fixtures so older-format lines stay parseable.
- The offset cursor makes tight polling correct — no same-second drops or
  re-reads that a `ts` cursor would cause.
- The binary is unchanged in behavior (smoke test still gates it) and still
  serves non-Go execers, so path A stays open as a fallback.

**Negative / accepted:**
- **A new SemVer obligation, permanently.** The Go API and `Record`'s JSON are
  now a supported surface; breaking either is a major bump. This is the cost of
  the choice, recorded here deliberately.
- **Runtime version skew across peers.** Consumers compiled against different
  library versions can run concurrently against the *same* log, so the on-disk
  schema must stay forward/backward compatible across versions — additive
  fields, absent-field defaults, as it already is ([[ADR-0001]], [[ADR-0004]]).
  This obligation is **not** specific to the library: exec-the-binary shares
  the same on-disk contract (it merely centralizes the writing code to one host
  binary). The library does not let us evolve the bytes more freely than exec
  would; that the struct compiles is not license to break the wire format.
- The import path `/channel` is load-bearing once pinned and awkward to change;
  fixed now to avoid a later rename.
- Presence upkeep moves to the consumer. A peer that forgets to drive
  `TouchPresence` will be reaped as stale — correct, but a sharper failure mode
  than the CLI's `stream` loop, which heartbeats automatically. Documented on
  the package.
- `cmd/agent-chat` gains an internal dependency on the new package; the build
  graph is one level deeper. Negligible — the package has no non-stdlib deps.
