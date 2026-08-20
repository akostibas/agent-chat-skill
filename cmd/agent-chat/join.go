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
	} else if _, err := join(as); err != nil {
		if errors.Is(err, channel.ErrNameTaken) {
			fmt.Fprintf(os.Stderr, "agent-chat: name %q is already active on channel %q — pick a different name with --as, or omit --as to be auto-named.\n", as, slug)
		} else {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		}
		os.Exit(1)
	}

	// Seed the read frontier at the join point: delivery (hook or wait) starts
	// from "now", and ADR-0011's bounce logic sees an honest frontier instead
	// of the conservative 0 a never-subscribed peer would report. The same
	// offset seeds the registry's frontier mirror.
	var seedOff int64
	if off, ok := c.ReadOffset(as); ok {
		seedOff = off
	} else if end, err := c.End(); err == nil {
		_ = c.SaveReadOffset(as, end.Offset())
		seedOff = end.Offset()
	}
	registerSession(channelRoot(), slug, as, seedOff)

	waitCmd := subscribeCmd(slug, as)

	if generated {
		fmt.Printf("Joined channel %q as %q (auto-generated — no --as given).\n", slug, as)
		fmt.Printf("Use %q as your name from now on, and tell the user.\n\n", as)
	} else {
		fmt.Printf("Joined channel %q as %q.\n\n", slug, as)
	}

	// Delivery is the hook's job; the doorbell only wakes an idle session
	// (ADR-0016). Without the hook installed there is no delivery at all, so
	// say that first — arming a doorbell alone would wake the agent with
	// nothing to read.
	if currentSessionID() == "" {
		fmt.Printf("No Claude Code session detected, so nothing will be delivered to you\n")
		fmt.Printf("automatically — poll for messages with history.sh.\n")
		return
	}

	if !hookInstalled() {
		fmt.Printf("NOT SUBSCRIBED: the agent-chat delivery hook is not installed, so peer\n")
		fmt.Printf("messages will NOT reach you — only `history.sh` can show them.\n\n")
		fmt.Printf("TELL YOUR USER: running `%s hook install` registers it in\n", selfInvocation())
		fmt.Printf("~/.claude/settings.json; newly started sessions then subscribe by joining\n")
		fmt.Printf("alone. It edits their settings, so it is their call — do not do it yourself.\n")
		return
	}

	fmt.Printf("You are SUBSCRIBED: the agent-chat delivery hook is installed, so new peer\n")
	fmt.Printf("messages will be injected into your context automatically as you work.\n\n")
	fmt.Printf("Now ARM YOUR IDLE DOORBELL (one Bash call, run_in_background=true):\n")
	fmt.Printf("  %s\n", waitCmd)
	fmt.Printf("It blocks silently, keeps your presence alive while you idle, and exits when\n")
	fmt.Printf("peer traffic arrives — waking you if you were idle. On ANY wake: make a tool\n")
	fmt.Printf("call (the hook injects the messages), then re-arm the same command. If it\n")
	fmt.Printf("ever dies, the hook reminds you to re-arm.\n")
}

// subscribeCmd builds the doorbell command an agent runs to stay reachable
// while idle, preferring the wait.sh shim next to this binary.
func subscribeCmd(slug, as string) string {
	cmd := ""
	if d := selfDir(); d != "" {
		shim := d + "/wait.sh"
		if _, err := os.Stat(shim); err == nil {
			cmd = fmt.Sprintf("bash %q %q %q", shim, slug, as)
		}
	}
	if cmd == "" {
		if exe, err := os.Executable(); err == nil {
			cmd = fmt.Sprintf("%q wait %q %q", exe, slug, as)
		} else {
			cmd = fmt.Sprintf("agent-chat wait %q %q", slug, as)
		}
	}
	return withChannelRoot(cmd, os.Getenv("AGENT_CHAT_ROOT"))
}

// withChannelRoot prefixes a printed command with the AGENT_CHAT_ROOT env
// assignment when the channel lives under a non-default root (e.g. an
// ephemeral container-fleet root). Without it the command resolves the channel
// under the global root and fails with "no such channel". join already runs
// under the right env, so it propagates that root verbatim. An empty root (the
// default global location) is left untouched. See issue #18.
func withChannelRoot(cmd, root string) string {
	if root == "" {
		return cmd
	}
	return fmt.Sprintf("AGENT_CHAT_ROOT=%q %s", root, cmd)
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
