package main

import (
	"fmt"
	"io"
	"os"
	"strings"
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

	c := newChannel(slug)
	if d := selfDir(); d != "" {
		go checkForUpdate(d)
	}

	if err := c.ensureDir(); err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}

	lockF, err := c.acquireLock(5e9)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}

	members := c.members()
	mentions := filterMentions(extractMentions(body), members)

	err = c.appendRecord(Record{
		Ts:       isoNow(),
		Sender:   as,
		Cwd:      agentCwd(),
		Branch:   agentBranch(),
		Kind:     "msg",
		Body:     body,
		Mentions: mentions,
	})
	releaseLock(lockF)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}

	c.reapStalePeers(as)

	fmt.Printf("sent (%d bytes) to %q as %q\n", len(raw), slug, as)
}
