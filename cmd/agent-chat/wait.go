package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

// The signal-mode doorbell (issue #60): a background process whose ONLY jobs
// are to exit — waking an idle agent through the harness's process-exit
// trigger — when wake-worthy traffic lands, and to carry the peer's presence
// heartbeat while blocked so an idle-but-open session never reads as departed.
// It never prints message content and never touches the read frontier: the
// delivery hook (hook.go) is the single deliverer and single frontier writer,
// which is what makes delivery exactly-once by construction.
//
// While armed it holds an exclusive flock on a per-peer doorbell lockfile.
// The kernel releases the lock the moment the process dies — however it dies —
// so the hook can probe "is the doorbell armed?" with one open+flock and nag
// the agent to re-arm (see hookNag). A lockfile that has never existed means
// the agent never opted into a doorbell, and the hook stays quiet about it.

func doorbellDir(root string) string { return filepath.Join(root, "doorbells") }

func doorbellPath(root, slug, name string) string {
	// slug and name are validated idents (no path chars); "--" cannot appear in
	// either, so the pairing is unambiguous.
	return filepath.Join(doorbellDir(root), slug+"--"+name+".lock")
}

// lockDoorbell claims the doorbell for this process, returning the open file
// whose flock must be held for the process lifetime, or nil if the lockfile
// itself is unusable.
//
// It BLOCKS while another process holds the lock rather than refusing (#61).
// Refusing made re-arming unsafe in both directions: a duplicate arm exited
// immediately, and an exit is the wake mechanism, so "re-arm on any exit" fed
// itself a wake loop; meanwhile an abandoned lockfile made the hook's "re-arm
// it now" nag impossible to obey. Blocking makes a duplicate arm cost nothing
// but a parked process that takes over if the incumbent dies, so re-arming is
// always both safe and effective.
func lockDoorbell(root, slug, name string) *os.File {
	if os.MkdirAll(doorbellDir(root), 0755) != nil {
		return nil
	}
	f, err := os.OpenFile(doorbellPath(root, slug, name), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err == syscall.EINTR {
			continue // a signal landed mid-block; the handler owns the exit
		}
		if err != nil {
			_ = f.Close()
			return nil
		}
		return f
	}
}

// doorbellState reports whether a doorbell lockfile exists and whether a live
// process holds it. (exists, armed):
//
//	(false, _)    — the agent never armed a doorbell here; not our business.
//	(true, true)  — armed and alive.
//	(true, false) — armed once, now dead: the nag-worthy state.
func doorbellState(root, slug, name string) (exists, armed bool) {
	f, err := os.OpenFile(doorbellPath(root, slug, name), os.O_RDWR, 0)
	if err != nil {
		return false, false
	}
	defer func() { _ = f.Close() }()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return true, false
	}
	return true, true
}

// cmdWait arms this peer's doorbell. `--signal` is accepted and ignored: it
// was the flag that selected doorbell mode back when `wait` could also print
// messages, and live sessions still hold that invocation in their context.
// COMPAT: remove after 2026-09-19
func cmdWait(args []string) {
	fs := newFlagSet("wait", "<slug> <name>")
	fs.Bool("signal", false, "accepted and ignored (this is always signal mode)")
	pos := parse(fs, args)
	wantPositional(fs, pos, 2)
	slug, name := pos[0], pos[1]
	validateIdent("slug", slug)
	validateIdent("name", name)

	c := openChannel(slug)
	if !c.Exists() {
		fmt.Fprintf(os.Stderr, "agent-chat: no such channel: %s (run join first)\n", slug)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Armed before the lock, which blocks: a parked duplicate must still die on
	// signal. A signal here is NOT a departure — the session is still alive and
	// the hook still delivers. Exit silently; SessionEnd owns the real goodbye.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigs
		cancel()
		os.Exit(0)
	}()

	lock := lockDoorbell(channelRoot(), slug, name)
	if lock == nil {
		fmt.Fprintf(os.Stderr, "agent-chat: cannot open doorbell lockfile %s\n", doorbellPath(channelRoot(), slug, name))
		os.Exit(1)
	}
	defer func() { _ = lock.Close() }()

	// The doorbell carries presence while the agent idles — the fix for
	// idle-but-open sessions being reaped (observed live in #60's testing).
	// Refresh-only, NOT RunHeartbeat: a doorbell must never resurrect presence,
	// or one left armed across a leave.sh would rejoin the peer it retired.
	go signalHeartbeat(ctx, c, name)

	err := signalLoop(ctx, c, name)
	switch {
	case err == errRetired:
		fmt.Printf("(agent-chat: doorbell retired — %q is no longer present on %q (left or reaped); do not re-arm unless you re-join)\n", name, c.Slug())
	case err != nil && err != context.Canceled:
		fmt.Fprintf(os.Stderr, "agent-chat: wait: %v\n", err)
		os.Exit(1)
	default:
		fmt.Printf("(agent-chat: new activity on %q — make any tool call and the delivery hook will inject it; then re-arm this doorbell)\n", c.Slug())
	}
}

// errRetired reports the doorbell's peer is no longer a member — its presence
// file vanished via a deliberate leave or a reap — so the doorbell should die
// quietly rather than keep watch for a departed identity.
var errRetired = fmt.Errorf("doorbell retired: peer no longer present")

// signalHeartbeat keeps the peer's presence FRESH while the doorbell blocks —
// refresh-only, so a missing presence file stays missing — and carries the
// reap duty with the same wall-clock wake-skip as RunHeartbeat (issue #39): a
// tick gap wider than the stale window means the host slept, and reaping then
// would falsely evict live peers.
func signalHeartbeat(ctx context.Context, c *channel.Channel, name string) {
	beat := time.Duration(channel.HeartbeatSecs()) * time.Second
	stale := time.Duration(channel.StaleSecs()) * time.Second
	ticker := time.NewTicker(beat)
	defer ticker.Stop()
	last := time.Now().Round(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().Round(0)
			_ = c.RefreshPresence(name)
			if now.Sub(last) <= stale {
				c.ReapStale(name)
			}
			last = now
		}
	}
}

// signalLoop blocks until a wake-worthy record lands past the peer's frontier,
// then returns — WITHOUT delivering or advancing anything. The grace re-check
// is what keeps a busy agent's doorbell from ringing: on spotting traffic it
// pauses and re-reads the persisted frontier, and if the hook consumed the
// records in the meantime (the agent was active), it resumes blocking from the
// hook's frontier instead of exiting. Only a genuinely idle agent — no hook
// fire during the grace — gets the wake. The same re-check absorbs the
// re-arm race, where a fresh doorbell starts a beat before the hook of the
// arming tool call has advanced the frontier.
func signalLoop(ctx context.Context, c *channel.Channel, name string) error {
	grace := time.Duration(envInt("AGENT_CHAT_SIGNAL_GRACE_MS", 2500)) * time.Millisecond
	var cur channel.Cursor
	if off, ok := c.ReadOffset(name); ok {
		cur = channel.CursorAt(off)
	} else {
		end, err := c.End()
		if err != nil {
			return err
		}
		cur = end
	}
	// Presence is checked about once a second (every 10th quiet poll), not per
	// iteration — a ReadDir is heavier than the ReadSince stat.
	presenceEvery := 10
	sincePresence := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		sincePresence++
		if sincePresence >= presenceEvery {
			sincePresence = 0
			if !slices.Contains(c.Members(), name) {
				return errRetired
			}
		}
		recs, next, err := c.ReadSince(ctx, cur)
		if err != nil {
			return err
		}
		worthy := false
		for _, r := range recs {
			if hookWorthy(r, name) {
				worthy = true
				break
			}
		}
		if worthy {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(grace):
			}
			if off, ok := c.ReadOffset(name); ok && off > cur.Offset() {
				cur = channel.CursorAt(off)
				continue // the hook consumed it — agent is active, keep blocking
			}
			return nil
		}
		cur = next
		if len(recs) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}
