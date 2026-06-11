# ADR-0004 — Port to a single Go binary

**Status:** Accepted

## Context

agent-chat began as "pure shell — no binary to build," and that simplicity was
a deliberate feature. Over time the codebase accumulated complexity that shell
handles poorly:

- **Concurrency is load-bearing.** `_common.sh` carries advisory locking
  (`shlock`), a heartbeat loop, and stale-peer reaping with a lock-claim-before-
  post race guard (ADR-0003). This is exactly the class of logic that is hard to
  test and easy to get subtly wrong in bash.
- **Hidden portability gaps.** The skill depends on `shlock` (BSD/macOS-only),
  `jq`, and bash-isms, plus `stat -f` vs `stat -c` and `date -v` vs `date -d`
  divergence across platforms.
- **Testing is thin.** One end-to-end smoke test; no unit coverage of the tricky
  parts (lock contention, reap idempotency, mention parsing, the JSONL schema).
- **JSON via subprocess.** Every read/write shells out to `jq`. Native
  (de)serialization would be simpler and safer.

## Decision

Replace the shell scripts with a single statically-linked `agent-chat` binary
(Go, stdlib only, no external modules) with subcommands mirroring today's
scripts:

```
agent-chat join    <slug> --as <name>
agent-chat send    <slug> --as <name>   (body on stdin)
agent-chat history <slug> [--since <ts>]
agent-chat stream  <slug> <name>        (invoked by Monitor)
```

**On-disk contract is unchanged.** Same channel layout
(`<slug>/log`, `log.lock`, `presence/`), same JSONL record schema. A Go agent
and a shell agent could share a channel.

**Shell shims stay for one release (COMPAT: remove after 2026-09-01).** The
shims are thin `exec` wrappers so the Monitor command printed by `join` keeps
its `bash stream.sh slug name` form, preserving backward compatibility for
agents whose sessions were already open.

**Real unit tests** cover locking (acquire/release contention), reap
idempotency, mention parsing, and the JSON schema. The existing smoke test
remains the integration gate and now also builds the binary.

**`flock(2)` replaces `shlock`.** `syscall.Flock` is available on macOS and
Linux; locks are automatically released on process death (no stale-lock files
to clean up). The lock *file* is preserved to give the flock syscall a stable
inode to lock on.

## Alternatives Considered

### Stay shell

- **Pro:** No build step; works on any machine without Go.
- **Pro:** Proven across real sessions at v0.2.x/v0.3.x.
- **Con:** Testing the concurrency-sensitive parts (locking, reaping) requires
  process-level sleeps and races that are fragile and slow. The smoke test
  already tests reaping with `date -v` vs `date -d` portability workarounds.
- **Con:** `shlock` is not in the POSIX standard and not installed by default on
  Linux. A Linux user would need to install it separately.
- **Con:** Every JSON operation forks `jq`. JSONL lines with special characters
  require careful quoting.

### Incremental: extract only the lock/reap core to Go, keep shell front-ends

- **Pro:** Smaller migration surface; shell scripts remain as the primary entry
  point.
- **Con:** Split-brain: two languages with an FFI boundary. Maintaining a C-
  extension / cgo shared-library just for flock from bash is worse than a full
  port.
- **Con:** Mention parsing and JSON serialization would still live in shell/jq,
  leaving the testability gap open.

### Full Go port (chosen)

- **Pro:** Single language; stdlib only; no `jq`/`shlock`/bash dependency.
- **Pro:** Testable concurrency with Go's `-race` detector available.
- **Pro:** Genuine cross-platform (Linux/macOS) once `shlock` is removed.
- **Con:** Requires Go to build. `make install` now has a build step. Users
  without Go can't build from source; a future release should distribute
  prebuilt binaries per arch (darwin/arm64, darwin/amd64, linux/amd64).
  Tracked as an open item.
- **Con:** Shims add a thin indirection layer for one release cycle.

## Consequences

- `make install` now requires Go (checked by `make`, `smoke-test.sh`, and
  `update.sh`).
- `make test` runs `go test ./cmd/agent-chat/` (unit) then `bin/smoke-test.sh`
  (integration).
- The binary is installed as `skill/agent-chat` alongside the shims. The shims
  `exec` the binary; agents using the old `bash join.sh` / `bash send.sh` form
  continue to work transparently.
- The Monitor invocation contract (`bash stream.sh slug name`) is unchanged for
  this release. When the COMPAT shims are removed (v0.5.x or later), SKILL.md
  and the Monitor command printed by `join` will move to the native
  `agent-chat stream slug name` form. That change will be a minor version bump
  per the versioning policy.
- Prebuilt binary distribution (darwin/arm64, darwin/amd64, linux/amd64) is
  left for a follow-up release to keep this PR focused on the port itself.
