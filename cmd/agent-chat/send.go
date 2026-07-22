package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

// broadcastToken is the reserved @-mention that means "everyone in the channel".
// It is matched case-insensitively and is never a valid member name.
const broadcastToken = "all"

// resolveAudience derives a send's recipients from its body, given the live
// roster. Every send must name a REACHABLE audience explicitly:
//
//   - @all (case-insensitive) → broadcast: returns (nil, true); an empty
//     Mentions slice is what the stream treats as "deliver to everyone".
//   - one or more @<name> matching a present member → addressed: returns those
//     members (the stream delivers only to members it names).
//   - anything else (no @-mention, or @names that match no present member —
//     prose like @vercel/otel, a typo, or an absent peer) → returns
//     (nil, false): the caller must refuse. This is deliberate: it stops an
//     unaddressed message from spraying the channel AND stops a mis-addressed
//     one from silently reaching nobody. If a broadcast is truly wanted, the
//     sender says so with @all.
func resolveAudience(body string, members []string) (mentions []string, ok bool) {
	raw := channel.ExtractMentions(body)
	if len(raw) == 0 {
		return nil, false
	}
	for _, m := range raw {
		if strings.EqualFold(m, broadcastToken) {
			return nil, true // explicit broadcast
		}
	}
	present := channel.FilterMentions(raw, members)
	if len(present) == 0 {
		return nil, false // named only unknown/absent peers → refuse
	}
	return present, true
}

func cmdSend(args []string) {
	slug, as := parseSlugAs("send", args)

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: read stdin: %v\n", err)
		os.Exit(1)
	}
	body := strings.TrimRight(string(raw), "\n")
	if body == "" {
		fmt.Fprintln(os.Stderr, "agent-chat: empty message body (provide on stdin)")
		os.Exit(1)
	}

	c := openChannel(slug)
	if d := selfDir(); d != "" {
		checkForUpdate(d)
	}

	// Every send must name a reachable audience explicitly — either @all
	// (broadcast) or one or more @<name> that name a present member. A body with
	// no such audience is refused rather than silently broadcast (or silently
	// delivered to nobody), so a message can neither spray the channel by
	// accident nor vanish because it addressed only a typo/absent peer.
	members := c.Members()
	mentions, ok := resolveAudience(body, members)
	if !ok {
		fmt.Fprint(os.Stderr,
			"agent-chat: refusing to send: no reachable audience.\n"+
				"  Add @all to broadcast to every agent, or @<name> a present member.\n")
		if len(members) > 0 {
			fmt.Fprintf(os.Stderr, "  present members: %s\n", strings.Join(members, ", "))
		}
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Append(ctx, channel.Record{
		Sender:   as,
		Cwd:      agentCwd(),
		Branch:   agentBranch(),
		Kind:     "msg",
		Body:     body,
		Mentions: mentions,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}

	// A member that is actively sending is, by definition, alive — keep its
	// heartbeat fresh so peers don't falsely reap it between stream beats (issue
	// #29). Refresh-only: a send never resurrects an already-reaped member (it
	// must re-join for that), matching the heartbeat's tombstone semantics.
	//
	// We deliberately do NOT reap other peers here. This is a fresh CLI process
	// with no tick history, so it cannot tell a genuinely stale peer from one
	// that merely looks stale because the host just woke from sleep — no
	// gap-based wake guard can ever apply to a one-shot process. A send moments
	// after a wake would mass-reap live peers and fire the flap storm issue #39
	// is about. Reaping is the long-running stream's job (RunHeartbeat), which
	// has the tick history to be wake-aware.
	_ = c.RefreshPresence(as)

	fmt.Printf("sent (%d bytes) to %q as %q\n", len(raw), slug, as)
}
