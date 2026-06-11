package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var identRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

func validateIdent(kind, value string) {
	if !identRE.MatchString(value) {
		fmt.Fprintf(os.Stderr, "agent-chat: invalid %s %q (must match ^[a-zA-Z0-9_-]{1,40}$)\n", kind, value)
		os.Exit(1)
	}
}

func cmdJoin(args []string) {
	slug, as := parseSlugAs("join", args)

	c := newChannel(slug)
	sweepOldChannels(c.root)
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
	err = c.appendRecord(Record{
		Ts:     isoNow(),
		Sender: as,
		Cwd:    agentCwd(),
		Branch: agentBranch(),
		Kind:   "join",
		Body:   "joined channel",
	})
	releaseLock(lockF)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}

	// Build the Monitor command pointing at the stream.sh shim so it's
	// backward-compatible with sessions that have the old invocation style.
	streamCmd := ""
	if d := selfDir(); d != "" {
		shim := d + "/stream.sh"
		if _, err := os.Stat(shim); err == nil {
			streamCmd = fmt.Sprintf("bash %q %q %q", shim, slug, as)
		}
	}
	if streamCmd == "" {
		if exe, err := os.Executable(); err == nil {
			streamCmd = fmt.Sprintf("%q stream %q %q", exe, slug, as)
		} else {
			streamCmd = fmt.Sprintf("agent-chat stream %q %q", slug, as)
		}
	}

	fmt.Printf("Joined channel %q as %q.\n\n", slug, as)
	fmt.Printf("Now call the Monitor tool with EXACTLY these parameters:\n")
	fmt.Printf("  description: agent-chat:%s\n", slug)
	fmt.Printf("  persistent: true\n")
	fmt.Printf("  timeout_ms: 3600000\n")
	fmt.Printf("  command: %s\n\n", streamCmd)
	fmt.Printf("After that, peer messages will arrive automatically as notifications for the\n")
	fmt.Printf("rest of this session. Do not call Monitor again for this channel.\n")
}

// parseSlugAs parses the mixed positional/flag style used by all subcommands:
//
//	<slug> --as <name>
//
// The slug may appear before or after --as (the shell scripts always put it
// first, but accepting it anywhere is more robust).
func parseSlugAs(cmd string, args []string) (slug, as string) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--as" || args[i] == "-as":
			if i+1 < len(args) {
				as = args[i+1]
				i++
			}
		case strings.HasPrefix(args[i], "--as="):
			as = strings.TrimPrefix(args[i], "--as=")
		case !strings.HasPrefix(args[i], "-") && slug == "":
			slug = args[i]
		}
	}
	if slug == "" || as == "" {
		fmt.Fprintf(os.Stderr, "usage: agent-chat %s <slug> --as <name>\n", cmd)
		os.Exit(1)
	}
	validateIdent("slug", slug)
	validateIdent("name", as)
	return slug, as
}
