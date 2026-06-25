package main

import (
	"context"
	"errors"
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	join := func(name string) (string, error) {
		return c.Join(ctx, channel.Record{
			Sender: name,
			Cwd:    agentCwd(),
			Branch: agentBranch(),
			Kind:   "join",
			Body:   "joined channel",
		})
	}

	// --as is optional. Without it the binary owns the entropy and picks a
	// memorable machine-random name, sidestepping the LLM name-clustering that
	// caused the silent collisions in #16. A human-picked name that's already
	// active is rejected so the agent re-picks (suffixes confuse humans); a
	// generated collision just regenerates, since there's no human in the loop.
	generated := as == ""
	if generated {
		as = claimGeneratedName(join)
	} else if name, err := join(as); err != nil {
		if errors.Is(err, channel.ErrNameTaken) {
			fmt.Fprintf(os.Stderr, "agent-chat: name %q is already active on channel %q — pick a different name with --as, or omit --as to be auto-named.\n", as, slug)
		} else {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		}
		os.Exit(1)
	} else {
		as = name
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

	streamCmd = withChannelRoot(streamCmd, os.Getenv("AGENT_CHAT_ROOT"))

	if generated {
		fmt.Printf("Joined channel %q as %q (auto-generated — no --as given).\n", slug, as)
		fmt.Printf("Use %q as your name from now on, and tell the user.\n\n", as)
	} else {
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

// withChannelRoot prefixes a Monitor stream command with the AGENT_CHAT_ROOT
// env assignment when the channel lives under a non-default root (e.g. an
// ephemeral container-fleet root). Without it, stream.sh resolves the channel
// under the global root and fails with "no such channel". join already runs
// under the right env, so it propagates that root into the printed command
// verbatim. An empty root (the default global location) is left untouched.
// See issue #18.
func withChannelRoot(streamCmd, root string) string {
	if root == "" {
		return streamCmd
	}
	return fmt.Sprintf("AGENT_CHAT_ROOT=%q %s", root, streamCmd)
}

// claimGeneratedName retries machine-generated names until join claims one.
// Generated collisions are rare (~2300 base combinations) and there's no human
// to re-pick, so we regenerate rather than reject. Bounded so a saturated
// channel fails loudly instead of spinning forever.
func claimGeneratedName(join func(string) (string, error)) string {
	for range 20 {
		name, err := join(generateName())
		if err == nil {
			return name
		}
		if !errors.Is(err, channel.ErrNameTaken) {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Fprintln(os.Stderr, "agent-chat: could not find a free auto-generated name after 20 tries")
	os.Exit(1)
	return ""
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
