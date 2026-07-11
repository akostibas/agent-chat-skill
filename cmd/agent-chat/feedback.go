package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

// cmdFeedback dispatches the feedback-poll subcommands. The poll primitive lets
// a channel run a bounded round: open it, collect items from members, tally the
// deduped candidate list, and close with a terminal outcome. The dice-roll
// trigger (#33) and the file-to-GitHub coordinator flow (#34) build on top.
func cmdFeedback(args []string) {
	if len(args) == 0 {
		feedbackUsage()
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "open":
		feedbackOpen(rest)
	case "submit":
		feedbackSubmit(rest)
	case "tally":
		feedbackTally(rest)
	case "close":
		feedbackClose(rest)
	default:
		fmt.Fprintf(os.Stderr, "agent-chat: unknown feedback subcommand %q\n", sub)
		feedbackUsage()
		os.Exit(1)
	}
}

func feedbackUsage() {
	fmt.Fprintln(os.Stderr, `usage: agent-chat feedback <subcommand> [args]

Subcommands:
  open   <slug> --as <name>                       open a feedback round
  submit <slug> --as <name>                        (items on stdin, one per line)
  tally  <slug>                                    print deduped candidate items
  close  <slug> --as <name> --outcome <o>          o = filed | declined | empty`)
}

func feedbackOpen(args []string) {
	slug, as := parseSlugAs("feedback open", args)
	c := openChannel(slug)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roundID := generateRoundID()
	body := fmt.Sprintf("Feedback round open. Report agent-chat friction or process "+
		"improvements with: feedback submit %s --as <you> (one item per line on stdin).", slug)
	if err := c.OpenFeedbackRound(ctx, as, roundID, body); err != nil {
		if errors.Is(err, channel.ErrRoundOpen) {
			fmt.Fprintf(os.Stderr, "agent-chat: a feedback round is already open on %q\n", slug)
		} else {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Printf("Opened feedback round %s on %q as %q.\n", roundID, slug, as)
}

func feedbackSubmit(args []string) {
	slug, as := parseSlugAs("feedback submit", args)

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: read stdin: %v\n", err)
		os.Exit(1)
	}
	body := strings.TrimRight(string(raw), "\n")
	if strings.TrimSpace(body) == "" {
		fmt.Fprintln(os.Stderr, "agent-chat: no feedback items (provide one per line on stdin)")
		os.Exit(1)
	}

	c := openChannel(slug)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roundID, err := c.SubmitFeedback(ctx, as, body)
	if err != nil {
		if errors.Is(err, channel.ErrNoOpenRound) {
			fmt.Fprintf(os.Stderr, "agent-chat: no open feedback round on %q\n", slug)
		} else {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Printf("Submitted %d item(s) to round %s on %q as %q.\n", countItems(body), roundID, slug, as)
}

func feedbackTally(args []string) {
	slug := parseSlugOnly("feedback tally", args)
	c := openChannel(slug)

	items, err := c.TallyFeedback()
	if err != nil {
		if errors.Is(err, channel.ErrNoOpenRound) {
			fmt.Fprintf(os.Stderr, "agent-chat: no open feedback round on %q\n", slug)
		} else {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		}
		os.Exit(1)
	}
	if len(items) == 0 {
		fmt.Printf("No feedback items submitted to the open round on %q.\n", slug)
		return
	}
	fmt.Printf("Candidate feedback items for %q (%d):\n", slug, len(items))
	for i, it := range items {
		fmt.Printf("  %d. %s\n", i+1, it)
	}
}

func feedbackClose(args []string) {
	slug, as := parseSlugAs("feedback close", args)
	outcome := scanFlag(args, "outcome")
	switch outcome {
	case "filed", "declined", "empty":
		// ok
	case "":
		fmt.Fprintln(os.Stderr, "usage: agent-chat feedback close <slug> --as <name> --outcome <filed|declined|empty>")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "agent-chat: invalid --outcome %q (want filed|declined|empty)\n", outcome)
		os.Exit(1)
	}

	c := openChannel(slug)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roundID, err := c.CloseFeedbackRound(ctx, as, outcome)
	if err != nil {
		if errors.Is(err, channel.ErrNoOpenRound) {
			fmt.Fprintf(os.Stderr, "agent-chat: no open feedback round on %q\n", slug)
		} else {
			fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Printf("Closed feedback round %s on %q (outcome=%s) as %q.\n", roundID, slug, outcome, as)
}

// parseSlugOnly is the strict form for tally: slug required, no --as.
func parseSlugOnly(cmd string, args []string) string {
	slug, _ := scanSlugAs(args)
	if slug == "" {
		fmt.Fprintf(os.Stderr, "usage: agent-chat %s <slug>\n", cmd)
		os.Exit(1)
	}
	validateIdent("slug", slug)
	return slug
}

// scanFlag returns the value of --name / --name=value, or "" if absent.
func scanFlag(args []string, name string) string {
	long, eq := "--"+name, "--"+name+"="
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == long:
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(args[i], eq):
			return strings.TrimPrefix(args[i], eq)
		}
	}
	return ""
}

// countItems counts non-blank lines in a submission body.
func countItems(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// generateRoundID returns a short opaque round id ("r" + 8 hex chars) from the
// CSPRNG. Only per-channel uniqueness matters; cross-channel collisions are
// irrelevant. Mirrors the machine-entropy approach used for names (name.go).
func generateRoundID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "r00000000"
	}
	return "r" + hex.EncodeToString(b[:])
}
