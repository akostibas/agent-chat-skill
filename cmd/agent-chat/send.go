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
//   - @-token(s) present but none match a present member (prose like
//     @vercel/otel, a typo, an absent peer) → audienceRefuse: the caller must
//     refuse. An @-token signals intent to address someone, so a non-matching
//     one is a likely mistake, not a quiet note.
func resolveAudience(body string, members []string) (mentions []string, aud audience) {
	raw := channel.ExtractMentions(body)
	if len(raw) == 0 {
		return nil, audienceFYI // no addressing intent → quiet note
	}
	for _, m := range raw {
		if strings.EqualFold(m, broadcastToken) {
			return nil, audienceBroadcast
		}
	}
	present := channel.FilterMentions(raw, members)
	if len(present) == 0 {
		return nil, audienceRefuse // named only unknown/absent peers → refuse
	}
	return present, audienceDirected
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
	mentions, aud := resolveAudience(body, members)
	if aud == audienceRefuse {
		fmt.Fprint(os.Stderr,
			"agent-chat: refusing to send: your @-mention matched no present member.\n"+
				"  Fix the name, use @all to broadcast, or drop the @-mention to post a pull-only FYI.\n")
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
