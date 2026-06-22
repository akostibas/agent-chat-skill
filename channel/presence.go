package channel

import (
	"context"
	"fmt"
	"os"
	"time"
)

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

// TouchPresence refreshes this peer's heartbeat file, creating it on first
// call. The caller must invoke it on its own cadence — there is no background
// heartbeat — at an interval shorter than AGENT_CHAT_STALE_SECS, or peers will
// reap it as departed.
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

// RunHeartbeat keeps this peer present until ctx is canceled: it touches
// presence once up front, then on every AGENT_CHAT_HEARTBEAT_SECS tick refreshes
// its own presence and reaps any peer that has gone stale. It blocks until ctx
// is done, so callers run it in a goroutine and own its lifetime via the
// context. This is the canonical heartbeat loop — any long-running member (the
// stream runner, an embedding server) should drive presence through it rather
// than re-implementing the ticker. It does NOT remove presence or post a leave
// on exit; pair a clean shutdown with RemovePresence + Leave.
func (c *Channel) RunHeartbeat(ctx context.Context, name string) {
	heartbeatSecs := envInt("AGENT_CHAT_HEARTBEAT_SECS", defaultHeartbeatSecs)
	_ = c.TouchPresence(name)
	ticker := time.NewTicker(time.Duration(heartbeatSecs) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.TouchPresence(name)
			c.ReapStale(name)
		}
	}
}

// Join atomically claims an identity on the channel and records the arrival. It
// is the collision-safe entry point a joining peer should use instead of a bare
// Append: under the channel lock it (1) reads the live roster, (2) resolves a
// free name — the requested r.Sender if no active member holds it, otherwise the
// same name with a numeric suffix (name-2, name-3, …), (3) claims that name's
// presence file, and (4) appends the join record under the resolved name. It
// returns the name actually assigned; the caller MUST adopt it for every
// subsequent send, mention, presence touch, and the Monitor stream, because it
// may differ from what was requested.
//
// Claiming presence inside the same lock is what makes the check race-free: two
// processes joining the same name in the same instant are serialized by the
// flock, so the second sees the first's presence and suffixes. It also closes
// the window between join and stream start — a peer is a visible member the
// moment it joins, before its heartbeat loop exists — which a presence-only or
// log-only check would otherwise miss. Staleness still applies: a name held only
// by a timed-out (e.g. SIGKILLed) peer is free to reuse, so ghosts don't
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

	taken := make(map[string]bool)
	for _, m := range c.ActiveMembers() {
		taken[m] = true
	}
	name := disambiguate(r.Sender, taken)

	if err := c.TouchPresence(name); err != nil {
		return "", err
	}
	r.Sender = name
	if r.Ts == "" {
		r.Ts = isoNow()
	}
	if err := c.appendLocked(r); err != nil {
		return "", err
	}
	return name, nil
}

// disambiguate returns name unchanged if no active member holds it, otherwise
// the first free "name-N" suffix starting at -2. taken is the set of live member
// names; it is finite, so the loop always terminates.
func disambiguate(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", name, i)
		if !taken[cand] {
			return cand
		}
	}
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
				Body:   "left channel (timed out)",
			})
		}()
		cancel()
	}
}
