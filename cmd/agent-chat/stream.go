package main

import (
	"context"
	"fmt"
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

	// Heartbeat: refresh presence and reap stale peers on each tick.
	heartbeatSecs := envInt("AGENT_CHAT_HEARTBEAT_SECS", defaultHeartbeatSecs)
	_ = c.TouchPresence(name)
	go func() {
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
	}()

	// Tail the log from the current end — the same poll loop an external peer
	// would run — emitting peer messages to stdout.
	if err := tailAndEmit(ctx, c, name); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "agent-chat: stream: %v\n", err)
	}
}

// tailAndEmit polls the channel from its current end via ReadSince and writes
// filtered, formatted records to stdout. Blocks until ctx is canceled or a read
// error occurs.
func tailAndEmit(ctx context.Context, c *channel.Channel, me string) error {
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
			emitStreamRecord(r)
		}
		if len(recs) == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// emitStreamRecord writes one record to stdout in the same format as stream.sh:
//
//	sender │ [ts kind] cwd=... branch=...
//	sender │ <body line 1>
//	sender │ <body line 2>
func emitStreamRecord(r channel.Record) {
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
	fmt.Println(header.String())

	for _, bodyLine := range strings.Split(r.Body, "\n") {
		fmt.Println(r.Sender + " │ " + bodyLine)
	}
}
