package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

func cmdStream(args []string) {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: agent-chat stream <slug> <name>")
		os.Exit(1)
	}
	slug := args[0]
	name := args[1]
	validateIdent("slug", slug)
	validateIdent("name", name)

	c := openChannel(slug)
	if !c.Exists() {
		fmt.Fprintf(os.Stderr, "agent-chat: no such channel: %s (run join first)\n", slug)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Emit leave exactly once — covers both graceful signal and deferred exit.
	var leaveOnce sync.Once
	emitLeave := func() {
		leaveOnce.Do(func() {
			_ = c.Leave(name, "left channel")
		})
	}
	defer func() {
		emitLeave()
		_ = c.RemovePresence(name)
		_ = c.ClearReadOffset(name)
	}()

	// Intercept SIGINT/SIGTERM/SIGHUP so we announce departure before exit.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigs
		emitLeave()
		_ = c.RemovePresence(name)
		_ = c.ClearReadOffset(name)
		cancel()
		os.Exit(0)
	}()

	// Heartbeat: reassert presence and reap stale peers until ctx is canceled.
	// Pass the full identity Record so a heartbeat that self-heals after a false
	// reap (e.g. the host slept) re-announces this peer with its cwd/branch.
	go c.RunHeartbeat(ctx, channel.Record{
		Sender: name,
		Cwd:    agentCwd(),
		Branch: agentBranch(),
	})

	// Tail the log from the current end — the same poll loop an external peer
	// would run — emitting peer messages to stdout.
	if err := tailAndEmit(ctx, c, slug, name); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "agent-chat: stream: %v\n", err)
	}
}

// tailAndEmit polls the channel from its current end via ReadSince and writes
// filtered, formatted records to stdout. Blocks until ctx is canceled or a read
// error occurs.
//
// Sleep-flap debounce (issue #39): a stdout line here is a peer wake event, so a
// spurious departure costs a subscriber a turn. When the host sleeps and wakes,
// a live peer is briefly reaped and re-announces itself — a timed-out [leave]
// followed seconds later by a "reconnected" [join]. To keep that flap from ever
// waking a subscriber, a timed-out leave is held for holdWindow() rather than
// emitted at once; if the same peer rejoins within the window both records are
// dropped silently. A leave that outlives the window is a genuine departure and
// is emitted. Clean leaves and ordinary joins pass through immediately. This
// covers a flap from ANY source (the stream heartbeat, a one-shot send, an
// external Go peer), so it is the backstop even if a source-side reap slips
// through.
func tailAndEmit(ctx context.Context, c *channel.Channel, slug, me string) error {
	deb := newFlapDebouncer(holdWindow())
	cur, err := c.End()
	if err != nil {
		return err
	}
	// Persist the read frontier so that if this stream dies, the reaper can tell
	// which directed messages went unread and bounce them to their senders
	// (ADR-0011). Seed it at the start point, then advance it as records are
	// delivered below.
	_ = c.SaveReadOffset(me, cur.Offset())
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		recs, next, err := c.ReadSince(ctx, cur)
		if err != nil {
			return err
		}
		cur = next
		for _, r := range recs {
			if r.Sender == me {
				continue
			}
			// FYI is the quiet tier: it lives in the log for history/catch-up but
			// is never a wake event, so the live stream skips it entirely (ADR-0012).
			if r.Kind == channel.KindFYI {
				continue
			}
			// Mention filter: a directed record (msg or bounce) whose mentions
			// don't name me is skipped — a bounce is addressed to one sender, so
			// it must narrow like a msg or it would reach every peer. Non-msg
			// kinds (join/leave) go through the debouncer.
			if (r.Kind == "msg" || r.Kind == channel.KindBounce) && len(r.Mentions) > 0 && !slices.Contains(r.Mentions, me) {
				continue
			}
			for _, e := range deb.offer(r, time.Now()) {
				emitStreamRecord(os.Stdout, e, slug)
			}
		}
		// Advance the persisted read frontier past everything just consumed: the
		// stream has now surfaced every record addressed to me in this batch, so
		// if it dies here those messages should not bounce (ADR-0011).
		if len(recs) > 0 {
			_ = c.SaveReadOffset(me, cur.Offset())
		}
		// A held leave whose window elapsed with no reconnect is a real
		// departure — emit it. time.Now() carries a monotonic reading, so if the
		// host re-sleeps mid-hold the elapsed check (now.Sub(heldAt)) freezes
		// exactly as the reaped peer's heartbeat does, keeping the
		// reconnect-vs-expiry race fair across a second nap — the very clock
		// quirk behind the bug, working for us.
		for _, e := range deb.expired(time.Now()) {
			emitStreamRecord(os.Stdout, e, slug)
		}
		if len(recs) == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// holdWindow is how long a timed-out leave is withheld to see whether the peer
// reconnects. It is derived, not a constant: the leave is racing exactly one
// thing — the reaped peer's next heartbeat, which lands within one heartbeat
// interval of a wake — so two intervals gives a full cycle plus equal slack and
// tracks any AGENT_CHAT_HEARTBEAT_SECS override for free (issue #39).
func holdWindow() time.Duration {
	return 2 * time.Duration(channel.HeartbeatSecs()) * time.Second
}

// flapDebouncer decides, record by record, what a subscriber should actually be
// woken for. It withholds a timed-out [leave] for a hold window; if the same
// peer reconnects first the pair is a sleep flap and both are dropped, otherwise
// the leave is a genuine departure and surfaces when the window expires. Time is
// injected (callers pass now) so the policy is unit-testable without real clocks
// or sleeps. Not safe for concurrent use — tailAndEmit drives it from one
// goroutine.
type flapDebouncer struct {
	hold time.Duration
	held map[string]pendingLeave
}

type pendingLeave struct {
	rec    channel.Record
	heldAt time.Time
}

func newFlapDebouncer(hold time.Duration) *flapDebouncer {
	return &flapDebouncer{hold: hold, held: map[string]pendingLeave{}}
}

// offer feeds one record and returns what to emit now (nil to emit nothing). A
// reconnect cancels a held leave (drops both); a timed-out leave is withheld; a
// clean leave, an unmatched join, and every msg pass straight through.
func (d *flapDebouncer) offer(r channel.Record, now time.Time) []channel.Record {
	if r.Kind == "join" {
		if _, ok := d.held[r.Sender]; ok {
			delete(d.held, r.Sender)
			return nil
		}
	}
	if r.Kind == "leave" && r.Body == channel.LeaveBodyTimedOut {
		d.held[r.Sender] = pendingLeave{rec: r, heldAt: now}
		return nil
	}
	return []channel.Record{r}
}

// expired returns held leaves whose hold window has elapsed as of now, removing
// them from the pending set. now.Sub(heldAt) is monotonic when both carry a
// monotonic reading, which is what makes the hold freeze across a re-sleep.
func (d *flapDebouncer) expired(now time.Time) []channel.Record {
	var out []channel.Record
	for name, h := range d.held {
		if now.Sub(h.heldAt) >= d.hold {
			out = append(out, h.rec)
			delete(d.held, name)
		}
	}
	return out
}

// Rendered-size budgets for the notification path. The Claude Code harness
// clips Monitor events twice — each line at ~600 rendered chars and the whole
// event at ~2.5K rendered chars — where "rendered" counts the per-line
// "sender │ " prefix and HTML entity escaping (< > & become &lt; &gt; &amp;).
// Staying under both means peers receive messages whole instead of cut
// mid-word by the harness; oversize records are cut deliberately at a line
// boundary with an exact recovery command appended (issue #37).
const (
	lineBudget    = 400  // max rendered chars per emitted line, prefix included
	eventBudget   = 2000 // max rendered chars per record before self-truncating
	footerReserve = 250  // rendered chars held back for the recovery footer
)

// renderedWidth returns the size of s as the notification path renders it,
// counting entity-escaped runes at their escaped width.
func renderedWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch r {
	case '<', '>':
		return 4 // &lt; / &gt;
	case '&':
		return 5 // &amp;
	}
	return 1
}

// wrapLine splits line into pieces whose rendered width each fits budget,
// breaking at the last space where possible and mid-word otherwise.
func wrapLine(line string, budget int) []string {
	if budget < 5 { // degenerate prefix wider than the budget; emit as-is
		return []string{line}
	}
	var out []string
	runes := []rune(line)
	for len(runes) > 0 {
		w, cut, lastSpace := 0, len(runes), -1
		for i, r := range runes {
			rw := runeWidth(r)
			if w+rw > budget {
				cut = i
				break
			}
			w += rw
			if r == ' ' {
				lastSpace = i
			}
		}
		if cut == len(runes) {
			out = append(out, string(runes))
			break
		}
		if lastSpace > 0 {
			out = append(out, string(runes[:lastSpace]))
			runes = runes[lastSpace+1:]
		} else {
			out = append(out, string(runes[:cut]))
			runes = runes[cut:]
		}
	}
	return out
}

// emitStreamRecord writes one record to w in the same format as stream.sh:
//
//	sender │ [ts kind] cwd=... branch=...
//	sender │ <body line 1>
//	sender │ <body line 2>
//
// Body lines are wrapped to lineBudget rendered chars, and the whole record is
// capped at eventBudget: past it, emission stops at a line boundary and a
// footer names the exact history command that recovers the full text.
func emitStreamRecord(w io.Writer, r channel.Record, slug string) {
	var header strings.Builder
	header.WriteString(r.Sender)
	header.WriteString(" │ [")
	header.WriteString(r.Ts)
	header.WriteString(" ")
	header.WriteString(r.Kind)
	header.WriteString("]")
	if r.Cwd != "" {
		header.WriteString(" cwd=")
		header.WriteString(r.Cwd)
	}
	if r.Branch != "" {
		header.WriteString(" branch=")
		header.WriteString(r.Branch)
	}
	_, _ = fmt.Fprintln(w, header.String())

	prefix := r.Sender + " │ "
	prefixW := renderedWidth(prefix)

	var lines []string
	for _, bodyLine := range strings.Split(r.Body, "\n") {
		lines = append(lines, wrapLine(bodyLine, lineBudget-prefixW)...)
	}

	total := renderedWidth(header.String())
	fits := total
	for _, l := range lines {
		fits += prefixW + renderedWidth(l)
	}

	for i, l := range lines {
		lw := prefixW + renderedWidth(l)
		if fits > eventBudget && total+lw > eventBudget-footerReserve {
			_, _ = fmt.Fprintf(w, "%s…(agent-chat: truncated — %d more lines; full text: history.sh %s --since %s)\n",
				prefix, len(lines)-i, slug, r.Ts)
			return
		}
		_, _ = fmt.Fprintln(w, prefix+l)
		total += lw
	}
}
