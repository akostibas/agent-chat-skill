package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

var identRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

func validateIdent(kind, value string) {
	if !identRE.MatchString(value) {
		fmt.Fprintf(os.Stderr, "agent-chat: invalid %s %q (must match ^[a-zA-Z0-9_-]{1,40}$)\n", kind, value)
		os.Exit(1)
	}
}

func cmdJoin(args []string) {
	slug, as := parseJoinArgs(args)

	c := openChannel(slug)
	sweepOldChannels(channelRoot())
	if d := selfDir(); d != "" {
		checkForUpdate(d)
	}

	// --as is optional: with no name, the binary owns the entropy and picks a
	// memorable machine-random one, sidestepping the LLM name-clustering that
	// caused the silent collisions in #16.
	requested := as
	generated := as == ""
	if generated {
		as = generateName()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Join (not Append) claims the name race-free under the channel lock and
	// returns the name actually assigned — which may be suffixed if the requested
	// one was already active.
	assigned, err := c.Join(ctx, channel.Record{
		Sender: as,
		Cwd:    agentCwd(),
		Branch: agentBranch(),
		Kind:   "join",
		Body:   "joined channel",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}
	as = assigned

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

	// Surface the assigned name prominently — the agent must re-address itself if
	// it was generated or reassigned.
	switch {
	case generated:
		fmt.Printf("Joined channel %q as %q (auto-generated — no --as given).\n", slug, as)
		fmt.Printf("Use %q as your name from now on, and tell the user.\n\n", as)
	case assigned != requested:
		fmt.Printf("Joined channel %q as %q.\n", slug, as)
		fmt.Printf("NOTE: %q was already active here, so you were renamed to %q.\n", requested, as)
		fmt.Printf("Use %q for every send, mention, and the Monitor stream below.\n\n", as)
	default:
		fmt.Printf("Joined channel %q as %q.\n\n", slug, as)
	}
	fmt.Printf("Now call the Monitor tool with EXACTLY these parameters:\n")
	fmt.Printf("  description: agent-chat:%s\n", slug)
	fmt.Printf("  persistent: true\n")
	fmt.Printf("  timeout_ms: 3600000\n")
	fmt.Printf("  command: %s\n\n", streamCmd)
	fmt.Printf("After that, peer messages will arrive automatically as notifications for the\n")
	fmt.Printf("rest of this session. Do not call Monitor again for this channel.\n")
}

// scanSlugAs parses the mixed positional/flag style used by every subcommand:
//
//	<slug> --as <name>
//
// The slug may appear before or after --as (the shell scripts always put it
// first, but accepting it anywhere is more robust). It does no validation or
// required-field enforcement — callers layer that on, since join treats --as as
// optional while the rest require it.
func scanSlugAs(args []string) (slug, as string) {
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
	return slug, as
}

// parseSlugAs is the strict form for send/history: both slug and --as required.
func parseSlugAs(cmd string, args []string) (slug, as string) {
	slug, as = scanSlugAs(args)
	if slug == "" || as == "" {
		fmt.Fprintf(os.Stderr, "usage: agent-chat %s <slug> --as <name>\n", cmd)
		os.Exit(1)
	}
	validateIdent("slug", slug)
	validateIdent("name", as)
	return slug, as
}

// parseJoinArgs is join's form: slug required, --as optional. An empty as means
// "generate one"; cmdJoin fills it in.
func parseJoinArgs(args []string) (slug, as string) {
	slug, as = scanSlugAs(args)
	if slug == "" {
		fmt.Fprintln(os.Stderr, "usage: agent-chat join <slug> [--as <name>]")
		os.Exit(1)
	}
	validateIdent("slug", slug)
	if as != "" {
		validateIdent("name", as)
	}
	return slug, as
}
