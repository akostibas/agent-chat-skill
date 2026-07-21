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
	}()

	// Intercept SIGINT/SIGTERM/SIGHUP so we announce departure before exit.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigs
		emitLeave()
		_ = c.RemovePresence(name)
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
func tailAndEmit(ctx context.Context, c *channel.Channel, slug, me string) error {
	cur, err := c.End()
	if err != nil {
		return err
	}
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
			// Mention filter: a msg with non-empty mentions that doesn't name me
			// is skipped. Non-msg kinds (join/leave) always pass through.
			if r.Kind == "msg" && len(r.Mentions) > 0 && !slices.Contains(r.Mentions, me) {
				continue
			}
			emitStreamRecord(os.Stdout, r, slug)
		}
		if len(recs) == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
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
