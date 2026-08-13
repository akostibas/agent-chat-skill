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

	// Feedback poll (#33): roll the die once here. Only a join that *creates* the
	// channel acts on the result (JoinNew opens the round iff it created the
	// channel), so passing the request to every join is harmless — a non-creator's
	// roll is discarded. That keeps the effective rate a true per-channel 10%
	// (one creator per channel) instead of compounding across joiners.
	var pollOpen *channel.PollOpen
	if feedbackRoll(envFloat("AGENT_CHAT_FEEDBACK_RATE", defaultFeedbackRate)) {
		pollOpen = &channel.PollOpen{RoundID: generateRoundID(), Body: feedbackOpenBody(slug)}
	}
	join := func(name string) (channel.JoinResult, error) {
		return c.JoinNew(ctx, channel.Record{
			Sender: name,
			Cwd:    agentCwd(),
			Branch: agentBranch(),
			Kind:   "join",
			Body:   "joined channel",
		}, pollOpen)
	}

	// --as is optional. Without it the binary owns the entropy and picks a
	// memorable machine-random name, sidestepping the LLM name-clustering that
	// caused the silent collisions in #16. A human-picked name that's already
	// active is rejected so the agent re-picks (suffixes confuse humans); a
	// generated collision just regenerates, since there's no human in the loop.
	generated := as == ""
	var res channel.JoinResult
	if generated {
		res = claimGeneratedName(join)
	} else if r, err := join(as); err != nil {
		if errors.Is(err, channel.ErrNameTaken) {
			fmt.Fprintf(os.Stderr, "agent-chat: name %q is already active on channel %q — pick a different name with --as, or omit --as to be auto-named.\n", as, slug)
		} else {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		}
		os.Exit(1)
	} else {
		res = r
	}
	as = res.Name

	// Build the Monitor command pointing at the stream.sh shim so it's
	// backward-compatible with sessions that have the old invocation style.
	streamCmd := subscribeCmd("stream", slug, as)
	waitCmd := subscribeCmd("wait", slug, as)

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
	fmt.Printf("rest of this session. Do not call Monitor again for this channel.\n\n")
	fmt.Printf("NO Monitor tool in your session? Subscribe with the wait fallback instead:\n")
	fmt.Printf("  1. Run this with the Bash tool, run_in_background=true:\n")
	fmt.Printf("       %s\n", waitCmd)
	fmt.Printf("  2. It blocks silently, then exits printing new peer messages — when it\n")
	fmt.Printf("     exits, read them and IMMEDIATELY re-run the same command in the\n")
	fmt.Printf("     background. Repeat every time it exits. No messages are lost in the\n")
	fmt.Printf("     gap; a slow re-arm just delays your presence heartbeat.\n")

	printFeedbackNudge(c, slug, as)
}

// subscribeCmd builds the command an agent runs to subscribe via the given
// subcommand ("stream" for Monitor, "wait" for the Monitor-free fallback),
// preferring the matching .sh shim next to this binary and propagating a
// non-default channel root.
func subscribeCmd(sub, slug, as string) string {
	cmd := ""
	if d := selfDir(); d != "" {
		shim := d + "/" + sub + ".sh"
		if _, err := os.Stat(shim); err == nil {
			cmd = fmt.Sprintf("bash %q %q %q", shim, slug, as)
		}
	}
	if cmd == "" {
		if exe, err := os.Executable(); err == nil {
			cmd = fmt.Sprintf("%q %s %q %q", exe, sub, slug, as)
		} else {
			cmd = fmt.Sprintf("agent-chat %s %q %q", sub, slug, as)
		}
	}
	return withChannelRoot(cmd, os.Getenv("AGENT_CHAT_ROOT"))
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
func claimGeneratedName(join func(string) (channel.JoinResult, error)) channel.JoinResult {
	for range 20 {
		res, err := join(generateName())
		if err == nil {
			return res
		}
		if !errors.Is(err, channel.ErrNameTaken) {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Fprintln(os.Stderr, "agent-chat: could not find a free auto-generated name after 20 tries")
	os.Exit(1)
	return channel.JoinResult{}
}

// printFeedbackNudge tells the agent to submit feedback when — and only when — a
// round is open on the channel, whether this join opened it (#33) or an earlier
// one did and this join inherited it.
func printFeedbackNudge(c *channel.Channel, slug, as string) {
	round, err := c.CurrentRound()
	if err != nil || round == nil || !round.Open {
		return
	}
	fmt.Printf("\nA feedback round is open on this channel. If agent-chat added any friction,\n")
	fmt.Printf("or you have a process improvement, submit it (one item per line on stdin):\n")
	fmt.Printf("  %s\n", feedbackSubmitCmd(slug, as))
}

// feedbackSubmitCmd builds the command an agent runs to submit feedback,
// preferring the feedback.sh shim next to this binary and propagating a
// non-default channel root, mirroring how the Monitor stream command is built.
func feedbackSubmitCmd(slug, as string) string {
	root := os.Getenv("AGENT_CHAT_ROOT")
	if d := selfDir(); d != "" {
		shim := d + "/feedback.sh"
		if _, err := os.Stat(shim); err == nil {
			return withChannelRoot(fmt.Sprintf("bash %q submit %q --as %q", shim, slug, as), root)
		}
	}
	if exe, err := os.Executable(); err == nil {
		return withChannelRoot(fmt.Sprintf("%q feedback submit %q --as %q", exe, slug, as), root)
	}
	return fmt.Sprintf("agent-chat feedback submit %q --as %q", slug, as)
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
