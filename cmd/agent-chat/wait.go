package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/akostibas/agent-chat-skill/channel"
)

// cmdWait is the Monitor-free subscription path: a one-shot block-until-woken
// consumer for harnesses whose only wake primitive is "a background command
// exited". It keeps this peer's presence heartbeat alive while blocked, prints
// the first batch of wake-worthy records (same filtering and flap debounce as
// stream), then exits 0 WITHOUT posting a leave — the subscriber is expected
// to read the output and immediately re-arm the same command. Presence
// survives the wake→re-arm gap because the presence file stays in place; if
// the gap outlives the stale threshold, the next wait's heartbeat self-heals
// with a re-announce, and the peers' flap debounce keeps that quiet.
//
// The read frontier persisted by SaveReadOffset is what makes the loop
// lossless: each wait seeds from the previous one's frontier, so a message
// that lands between exit and re-arm is delivered by the next wait instantly.
// A signal (session close, user kill) is a genuine departure and does post
// leave, exactly like stream.
func cmdWait(args []string) {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: agent-chat wait <slug> <name>")
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

	var leaveOnce sync.Once
	depart := func() {
		leaveOnce.Do(func() {
			_ = c.Leave(name, "left channel")
			_ = c.RemovePresence(name)
			_ = c.ClearReadOffset(name)
			deregisterSession(channelRoot(), slug)
		})
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigs
		depart()
		cancel()
		os.Exit(0)
	}()

	go c.RunHeartbeat(ctx, channel.Record{
		Sender: name,
		Cwd:    agentCwd(),
		Branch: agentBranch(),
	})

	// Resume from the previous wait's frontier; a first arm (no frontier yet)
	// starts at the end, mirroring stream — history is for catch-up, not wakes.
	var seed channel.Cursor
	if off, ok := c.ReadOffset(name); ok {
		seed = channel.CursorAt(off)
	} else {
		end, err := c.End()
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-chat: wait: %v\n", err)
			os.Exit(1)
		}
		seed = end
	}

	if err := tailAndEmit(ctx, c, slug, name, seed, true); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "agent-chat: wait: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("(agent-chat: re-arm now — run this same wait command in the background again to keep listening)")
}
