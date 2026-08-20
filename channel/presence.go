package channel

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"
)

// ErrNameTaken is returned by Join when the requested name is already held by an
// active member. The caller decides policy: a human-picked name should surface
// this and re-pick; a machine-generated one should regenerate and retry.
var ErrNameTaken = errors.New("channel: name already active")

// Members returns the names of all peers with a presence file. A peer becomes a
// member by calling TouchPresence and stops being one when its file is removed
// (RemovePresence on a clean leave, or ReapStale after it goes stale).
func (c *Channel) Members() []string {
	entries, err := os.ReadDir(c.presDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// ActiveMembers returns the names of members whose heartbeat is still fresh —
// presence file mod-time within AGENT_CHAT_STALE_SECS. It is the read-only
// counterpart to Members (which lists every presence file regardless of age)
// and to ReapStale (which retires stale peers, but mutates the log and takes
// the lock). Use ActiveMembers to answer "who is live here right now?" without
// side effects — e.g. to gate a feature on a peer actually being present.
//
// Staleness uses the same AGENT_CHAT_STALE_SECS threshold ReapStale honors, so
// a peer ActiveMembers omits is exactly one a reaper would (eventually) retire.
func (c *Channel) ActiveMembers() []string {
	staleSecs := envInt("AGENT_CHAT_STALE_SECS", defaultStaleSecs)
	entries, err := os.ReadDir(c.presDir())
	if err != nil {
		return nil
	}
	now := time.Now()
	cutoff := time.Duration(staleSecs) * time.Second
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) > cutoff {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

// TouchPresence registers this peer as a member, creating its heartbeat file on
// first call and refreshing the mtime thereafter. It is the explicit "I am
// joining" act: Join calls it under lock, and an external Go peer calls it to
// register. Because it creates, it also *resurrects* — do not use it for
// routine heartbeats of an already-registered peer, or a member the sweep has
// reaped would silently reappear with no [join] record (see RefreshPresence and
// issue #29). The caller must refresh on its own cadence — there is no
// background heartbeat — shorter than AGENT_CHAT_STALE_SECS, or peers reap it.
func (c *Channel) TouchPresence(name string) error {
	if err := os.MkdirAll(c.presDir(), 0755); err != nil {
		return err
	}
	pf := c.presFile(name)
	now := time.Now()
	if os.Chtimes(pf, now, now) != nil {
		f, err := os.OpenFile(pf, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return err
		}
		return f.Close()
	}
	return nil
}

// RefreshPresence bumps this peer's heartbeat mtime *only if it is already
// registered* — it never creates the file. This is the routine-heartbeat
// counterpart to TouchPresence: once the stale sweep has reaped a peer (removed
// its presence file and posted a [leave]), a bare heartbeat must not resurrect
// it, or the sweep re-reaps it on the next pass and the same departure is
// re-announced forever with no intervening [join] (issue #29). A reaped member
// rejoins only by an explicit TouchPresence/Join (or EnsurePresence + a [join]),
// which logs a real arrival. A missing file is not an error — the peer is simply
// gone until it re-registers.
func (c *Channel) RefreshPresence(name string) error {
	now := time.Now()
	if err := os.Chtimes(c.presFile(name), now, now); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// EnsurePresence guarantees this peer has a fresh presence file: it bumps the
// mtime if the file exists and creates it if it does not, returning created ==
// true exactly when it had to create it — i.e. when the peer had been reaped (or
// never registered). It is the self-healing counterpart to RefreshPresence: a
// live process's own heartbeat is authoritative for its own presence, so a peer
// that was falsely reaped (e.g. every peer looked stale at once after the host
// slept) can reassert itself instead of silently staying gone.
//
// A created == true return is the caller's cue to announce a fresh [join] before
// heartbeating on, so the log carries an intervening arrival and peers see the
// return. That is what dodges the issue #29 re-reap loop: that bug was bare
// resurrection with no [join] (a reaper re-announces the same departure
// forever); pairing the re-create with a real [join] means any flapping shows
// honest join/leave pairs, and a genuinely live peer that keeps heartbeating is
// never reaped again in the first place. RunHeartbeat drives this.
func (c *Channel) EnsurePresence(name string) (created bool, err error) {
	if err := os.MkdirAll(c.presDir(), 0755); err != nil {
		return false, err
	}
	now := time.Now()
	if os.Chtimes(c.presFile(name), now, now) == nil {
		return false, nil // already registered — just a refresh
	}
	f, err := os.OpenFile(c.presFile(name), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return false, err
	}
	return true, f.Close()
}

// RunHeartbeat keeps a member present until ctx is canceled: on every
// AGENT_CHAT_HEARTBEAT_SECS tick it reasserts its own presence (EnsurePresence)
// and reaps any peer that has gone stale. It blocks until ctx is done, so callers
// run it in a goroutine and own its lifetime via the context. This is the
// canonical heartbeat loop — any long-running member (the stream runner, an
// embedding server) should drive presence through it rather than re-implementing
// the ticker.
//
// It takes the peer's identity Record (Sender, plus Cwd/Branch for the log) so it
// can self-heal: if a tick finds its own presence file gone — reaped while the
// host was asleep, say — it re-creates it AND posts a fresh [join] carrying that
// Record, so a live process is never silently absent. A live peer that keeps
// beating is never reaped, so this stays quiet in steady state; only a real
// reap-then-survive produces an honest join/leave pair (see EnsurePresence and
// issue #29).
//
// Reaping is wake-aware. When the wall clock jumps far more than one heartbeat
// interval between ticks — the signature of the host suspending and resuming —
// every peer's mtime looks stale at once, so reaping this tick would falsely
// evict live peers before their own heartbeats get a chance to refresh. On such
// a tick RunHeartbeat refreshes but skips the reap; the next normal-gap tick
// reaps as usual, by which point live peers have re-touched and only the truly
// dead remain stale.
//
// It does NOT remove presence or post a leave on exit; pair a clean shutdown with
// RemovePresence + Leave.
func (c *Channel) RunHeartbeat(ctx context.Context, r Record) {
	heartbeatSecs := envInt("AGENT_CHAT_HEARTBEAT_SECS", defaultHeartbeatSecs)
	staleSecs := envInt("AGENT_CHAT_STALE_SECS", defaultStaleSecs)
	name := r.Sender
	_ = c.RefreshPresence(name)
	ticker := time.NewTicker(time.Duration(heartbeatSecs) * time.Second)
	defer ticker.Stop()
	// last/now are stripped of their monotonic reading (Round(0)) so the gap
	// heartbeatTick sees is measured on the WALL clock — the same basis as the
	// mtime-age predicate it guards. This is load-bearing on macOS: Go's
	// monotonic clock is mach_absolute_time, which pauses across system sleep, so
	// a monotonic gap reads ~one beat even after a 20-minute nap and the wake-skip
	// never fires; the wall gap correctly shows the sleep and trips the skip
	// (issue #39). The ticker itself stays monotonic on purpose — it should fire
	// on awake-time cadence, not replay a beat per slept second.
	last := time.Now().Round(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().Round(0)
			c.heartbeatTick(r, now.Sub(last), staleSecs)
			last = now
		}
	}
}

// heartbeatTick performs one beat: reassert own presence (re-announcing a
// [join] if it had been reaped) and, unless the host just woke, reap stale
// peers. gap is the wall-clock time since the previous tick. A gap wider than
// the whole stale window is never a normal heartbeatSecs tick — it means the
// clock jumped forward, i.e. the host suspended and resumed — so every peer's
// mtime looks stale at once; reaping then would falsely evict live peers before
// their own heartbeats refresh, so this tick skips the reap. Split out from the
// loop so the wake-skip is deterministically testable without a controllable
// clock.
func (c *Channel) heartbeatTick(r Record, gap time.Duration, staleSecs int) {
	if created, _ := c.EnsurePresence(r.Sender); created {
		c.announceRejoin(r)
	}
	if gap <= time.Duration(staleSecs)*time.Second {
		c.ReapStale(r.Sender)
	}
}

// announceRejoin posts a [join] on behalf of a peer whose presence was recreated
// after a reap, so the log carries an intervening arrival (see EnsurePresence).
// Best-effort: a failed append just means the visible re-announcement is missed,
// not that the peer is absent — its presence file already exists.
func (c *Channel) announceRejoin(r Record) {
	r.Kind = "join"
	r.Body = RejoinBody
	r.Ts = ""
	r.Mentions = nil
	_ = c.Append(context.Background(), r)
}

// Join atomically claims an identity on the channel and records the arrival. It
// is the collision-safe entry point a joining peer should use instead of a bare
// Append: under the channel lock it (1) reads the live roster, (2) refuses with
// ErrNameTaken if r.Sender is already an active member, otherwise (3) claims that
// name's presence file and (4) appends the join record. It returns the claimed
// name on success.
//
// Claiming presence inside the same lock is what makes the check race-free: two
// processes joining the same name in the same instant are serialized by the
// flock, so the second sees the first's presence and gets ErrNameTaken. It also
// closes the window between join and stream start — a peer is a visible member
// the moment it joins, before its heartbeat loop exists — which a presence-only
// or log-only check would otherwise miss. Staleness still applies: a name held
// only by a timed-out (e.g. SIGKILLed) peer is free to reclaim, so ghosts don't
// permanently burn names.
func (c *Channel) Join(ctx context.Context, r Record) (string, error) {
	if err := c.ensureDir(); err != nil {
		return "", err
	}
	lockF, err := c.acquireLock(ctx)
	if err != nil {
		return "", err
	}
	defer releaseLock(lockF)

	if slices.Contains(c.ActiveMembers(), r.Sender) {
		return "", ErrNameTaken
	}
	if err := c.TouchPresence(r.Sender); err != nil {
		return "", err
	}
	if r.Ts == "" {
		r.Ts = isoNow()
	}
	if err := c.appendLocked(r); err != nil {
		return "", err
	}
	return r.Sender, nil
}

// RemovePresence deletes this peer's presence file. Call it alongside Leave on
// a clean shutdown so the peer is not later re-announced as a timed-out
// departure.
func (c *Channel) RemovePresence(name string) error {
	err := os.Remove(c.presFile(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Leave posts a leave record for name under lock. Use it to announce a clean
// departure; pair it with RemovePresence.
func (c *Channel) Leave(name, body string) error {
	return c.Append(context.Background(), Record{
		Sender: name,
		Kind:   "leave",
		Body:   body,
	})
}

// ReapStale posts a leave on behalf of any peer (other than me) whose heartbeat
// is older than AGENT_CHAT_STALE_SECS, and removes its presence file. The reap
// is claimed under lock by deleting the presence file before posting, so
// concurrent reapers across processes cannot double-post. Best-effort: errors
// are swallowed.
func (c *Channel) ReapStale(me string) {
	staleSecs := envInt("AGENT_CHAT_STALE_SECS", defaultStaleSecs)
	entries, err := os.ReadDir(c.presDir())
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		name := e.Name()
		if name == me {
			continue
		}
		pf := c.presFile(name)
		info, err := os.Stat(pf)
		if err != nil || now.Sub(info.ModTime()) <= time.Duration(staleSecs)*time.Second {
			continue
		}
		// Claim the reap under lock.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lockF, err := c.acquireLock(ctx)
		if err != nil {
			cancel()
			continue
		}
		func() {
			defer releaseLock(lockF)
			info2, err := os.Stat(pf)
			if err != nil {
				return // already reaped
			}
			if now.Sub(info2.ModTime()) <= time.Duration(staleSecs)*time.Second {
				return // peer refreshed
			}
			if os.Remove(pf) != nil {
				return // another reaper claimed it
			}
			_ = c.appendLocked(Record{
				Ts:     isoNow(),
				Sender: name,
				Kind:   "leave",
				Body:   LeaveBodyTimedOut,
			})
			// The peer left with directed messages possibly unread — tell their
			// senders, in the same locked section so departure and bounce arrive
			// together. Only reached on a reap the caller already deemed genuine
			// (heartbeatTick skips this on a wake tick), so a sleep flap does not
			// bounce (ADR-0011).
			c.bounceUndelivered(ctx, name)
			_ = c.ClearReadOffset(name)
		}()
		cancel()
	}
}
