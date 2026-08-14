package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

// broadcastToken is the reserved @-mention that means "everyone in the channel".
// It is matched case-insensitively and is never a valid member name.
const broadcastToken = "all"

// audience is the delivery tier a send resolves to — which peers, if any, the
// message should wake. It is derived purely from the body's addressing, so the
// tier and the interruption it costs are the sender's explicit choice.
type audience int

const (
	// audienceRefuse: the body @-mentioned someone, but no present member matched
	// — a typo, a package name like @vercel/otel, or an absent peer. Refused, so a
	// mis-addressed message fails loudly instead of reaching nobody.
	audienceRefuse audience = iota
	// audienceBroadcast: @all — wake every peer.
	audienceBroadcast
	// audienceDirected: one or more @<name> matching present members — wake those.
	audienceDirected
	// audienceFYI: no @-mention at all — post to the log but wake no one. A
	// deliberate quiet note, seen only when a peer pulls history (ADR-0012).
	audienceFYI
)

// resolveAudience derives a send's delivery tier from its body, given the live
// roster. The distinction that separates "quiet" from "mistake" is whether the
// body carries any addressing intent at all:
//
//   - no @-token whatsoever → audienceFYI: the sender addressed no one on
//     purpose, so the message is logged but wakes nobody (pull-only).
//   - @all (case-insensitive) → audienceBroadcast; an empty Mentions slice is
//     what the stream treats as "deliver to everyone".
//   - one or more @<name> matching a present member → audienceDirected with
//     those members (the stream delivers only to members it names).
//   - @-token(s) present but none match a present member (a typo, an absent
//     peer) → audienceRefuse: the caller must refuse. An @-token signals intent
//     to address someone, so a non-matching one is a likely mistake, not a quiet
//     note. An @-token that is merely quoted — wrapped in a markdown code span,
//     like `@dependabot rebase` — never reaches this scan at all, so quoting a
//     bot command or a scoped package sends without rewording (issue #57).
//
// unmatched carries the offending tokens when aud is audienceRefuse, so the
// refusal can quote back exactly what failed; it is nil otherwise.
func resolveAudience(body string, members []string) (mentions, unmatched []string, aud audience) {
	raw := channel.ExtractMentions(body)
	if len(raw) == 0 {
		return nil, nil, audienceFYI // no addressing intent → quiet note
	}
	for _, m := range raw {
		if strings.EqualFold(m, broadcastToken) {
			return nil, nil, audienceBroadcast
		}
	}
	present := channel.FilterMentions(raw, members)
	if len(present) == 0 {
		return nil, raw, audienceRefuse // named only unknown/absent peers → refuse
	}
	return present, nil, audienceDirected
}

// quoteTokens renders unmatched mention tokens back to the sender the way they
// were written, so the refusal names the actual offender instead of the generic
// "your @-mention".
func quoteTokens(tokens []string) string {
	switch len(tokens) {
	case 0:
		return "your @-mention" // defensive: refuse always carries ≥1 token
	case 1:
		return "@" + tokens[0]
	}
	q := make([]string, len(tokens))
	for i, t := range tokens {
		q[i] = "@" + t
	}
	return strings.Join(q, ", ")
}

// firstToken is the token the escape example is built from — showing the
// sender's own text is what makes the fix copy-pasteable.
func firstToken(tokens []string) string {
	if len(tokens) == 0 {
		return "@token" // defensive: refuse always carries ≥1 token
	}
	return "@" + tokens[0]
}

// nearestMember returns the present member an unmatched token most plausibly
// meant, or "" if none is close. It exists to order the refusal's advice, not to
// decide anything: guessing intent is a non-goal (ADR-0013), so a near miss
// still refuses — it just leads with "did you mean" instead of with the quoting
// escape. Leading with the wrong fix is the exact complaint issue #57 made about
// the old text, and it would be self-defeating to reintroduce it in the fix.
func nearestMember(tokens, members []string) string {
	best, bestDist := "", 3 // only a distance of 1 or 2 counts as "close"
	for _, t := range tokens {
		for _, m := range members {
			if d := editDistance(strings.ToLower(t), strings.ToLower(m)); d < bestDist {
				best, bestDist = m, d
			}
		}
	}
	return best
}

// editDistance is Levenshtein over bytes, bounded in practice by the 40-byte
// mention cap.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

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

	c := openChannel(slug)
	if d := selfDir(); d != "" {
		checkForUpdate(d)
	}

	// Resolve the delivery tier from the body's addressing (ADR-0010, ADR-0012).
	// A body that @-mentions someone unreachable is refused — a mis-address should
	// fail loudly, not vanish. A body with no @-mention at all is a deliberate
	// FYI: logged for catch-up but pull-only, so it wakes no one. @all and
	// @<name> deliver as before.
	members := c.Members()
	mentions, unmatched, aud := resolveAudience(body, members)
	if aud == audienceRefuse {
		fmt.Fprintf(os.Stderr,
			"agent-chat: refusing to send: no present member matches %s.\n",
			quoteTokens(unmatched))
		// Name the escape here, not only in SKILL.md — an escape nobody discovers
		// is not a fix (issue #57). Lead with whichever fix is likelier: a token
		// one or two edits off a present name is a typo, anything else is probably
		// content the sender wants to quote.
		if near := nearestMember(unmatched, members); near != "" {
			fmt.Fprintf(os.Stderr, "  Did you mean @%s?\n", near)
			fmt.Fprintf(os.Stderr,
				"  If you did mean it literally, wrap it in backticks to quote it: `%s`\n"+
					"  — quoted tokens address no one.\n", firstToken(unmatched))
		} else {
			fmt.Fprintf(os.Stderr,
				"  If you meant it literally (a bot command, a package name), wrap it in\n"+
					"  backticks to quote it: `%s` — quoted tokens address no one.\n"+
					"  Otherwise fix the name, use @all to broadcast, or drop the @ to post a\n"+
					"  pull-only FYI.\n", firstToken(unmatched))
		}
		if len(members) > 0 {
			fmt.Fprintf(os.Stderr, "  present members: %s\n", strings.Join(members, ", "))
		}
		os.Exit(2)
	}
	kind := "msg"
	if aud == audienceFYI {
		kind = channel.KindFYI
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Append(ctx, channel.Record{
		Sender:   as,
		Cwd:      agentCwd(),
		Branch:   agentBranch(),
		Kind:     kind,
		Body:     body,
		Mentions: mentions,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "agent-chat: %v\n", err)
		os.Exit(1)
	}

	// A member that is actively sending is, by definition, alive — keep its
	// heartbeat fresh so peers don't falsely reap it between stream beats (issue
	// #29). Refresh-only: a send never resurrects an already-reaped member (it
	// must re-join for that), matching the heartbeat's tombstone semantics.
	//
	// We deliberately do NOT reap other peers here. This is a fresh CLI process
	// with no tick history, so it cannot tell a genuinely stale peer from one
	// that merely looks stale because the host just woke from sleep — no
	// gap-based wake guard can ever apply to a one-shot process. A send moments
	// after a wake would mass-reap live peers and fire the flap storm issue #39
	// is about. Reaping is the long-running stream's job (RunHeartbeat), which
	// has the tick history to be wake-aware.
	_ = c.RefreshPresence(as)

	if aud == audienceFYI {
		fmt.Printf("posted FYI (%d bytes) to %q as %q — pull-only, no peer was notified\n", len(raw), slug, as)
	} else {
		fmt.Printf("sent (%d bytes) to %q as %q\n", len(raw), slug, as)
	}
}
