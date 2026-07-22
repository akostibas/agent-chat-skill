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

	mentions := channel.FilterMentions(channel.ExtractMentions(body), c.Members())

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
