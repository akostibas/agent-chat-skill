package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
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

	c := newChannel(slug)
	if _, err := os.Stat(c.logPath()); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "agent-chat: no such channel: %s (run join first)\n", slug)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Emit leave exactly once — covers both graceful signal and deferred exit.
	var leaveOnce sync.Once
	emitLeave := func() {
		leaveOnce.Do(func() {
			c.emitLeaveEvent(name, "left channel")
		})
	}
	defer func() {
		emitLeave()
		c.removePresence(name)
	}()

	// Intercept SIGINT/SIGTERM/SIGHUP so we announce departure before exit.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigs
		emitLeave()
		c.removePresence(name)
		cancel()
		os.Exit(0)
	}()

	// Heartbeat: refresh presence and reap stale peers on each tick.
	heartbeatSecs := envInt("AGENT_CHAT_HEARTBEAT_SECS", defaultHeartbeatSecs)
	c.touchPresence(name)
	go func() {
		ticker := time.NewTicker(time.Duration(heartbeatSecs) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.touchPresence(name)
				c.reapStalePeers(name)
			}
		}
	}()

	// Tail the log and emit peer messages to stdout.
	if err := tailAndEmit(ctx, c.logPath(), name); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "agent-chat: stream: %v\n", err)
	}
}

// tailAndEmit tails path from its current end and writes filtered, formatted
// lines to stdout. Blocks until ctx is canceled or a read error occurs.
func tailAndEmit(ctx context.Context, path, me string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	reader := bufio.NewReader(f)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err == io.EOF {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		var r Record
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		if r.Sender == me {
			continue
		}
		// Mention filter: if this is a msg with non-empty mentions and I'm not
		// in them, skip it. Non-msg kinds (join/leave) always pass through.
		if r.Kind == "msg" && len(r.Mentions) > 0 {
			mentioned := false
			for _, m := range r.Mentions {
				if m == me {
					mentioned = true
					break
				}
			}
			if !mentioned {
				continue
			}
		}

		emitStreamRecord(r)
	}
}

// emitStreamRecord writes one record to stdout in the same format as stream.sh:
//
//	sender │ [ts kind] cwd=... branch=...
//	sender │ <body line 1>
//	sender │ <body line 2>
func emitStreamRecord(r Record) {
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
