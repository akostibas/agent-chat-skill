package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

// cmdHook is the Claude Code hook entry point (issue #59): the harness invokes
// it on PostToolUse / UserPromptSubmit / SessionEnd for EVERY session on the
// machine, so the not-a-member path must stay near-free (one stat) and nothing
// here may ever fail loudly — a broken hook would tax every tool call the user
// makes. All errors degrade to silence; messages left undelivered are picked up
// by the next fire because the read frontier only advances on success.
func cmdHook(args []string) {
	if len(args) > 0 && args[0] == "install" {
		cmdHookInstall(args[1:])
		return
	}
	in := readHookInput(os.Stdin)
	sid := in.SessionID
	if sid == "" {
		sid = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	if !sessionIDRE.MatchString(sid) {
		return
	}
	root := channelRoot()
	path := sessionFile(root, sid)
	info, err := os.Stat(path)
	if err != nil {
		return // not a channel member: the common case, and the whole cost
	}
	ms := readMemberships(path)

	if in.HookEventName == "SessionEnd" {
		sessionEndLeave(root, ms)
		_ = os.Remove(path)
		return
	}

	// The registry mtime is bumped every fire, so the gap since the last fire
	// is this hook's wake detector: a gap wider than the stale window means the
	// host slept (or the session sat idle) and every peer's heartbeat looks
	// stale at once — reaping then would falsely evict live peers, exactly the
	// skip RunHeartbeat's wake-aware tick performs (issue #39).
	gap := time.Since(info.ModTime())
	reapOK := gap <= time.Duration(channel.StaleSecs())*time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var out strings.Builder
	var kept []membership
	for _, m := range ms {
		c, err := channel.Open(root, m.Slug)
		if err != nil || !c.Exists() {
			continue // channel swept or deleted: prune the membership
		}
		kept = append(kept, m)
		deliverChannel(ctx, &out, c, m, reapOK)
	}
	_ = writeMemberships(path, kept)

	if out.Len() == 0 {
		return
	}
	emitHookOutput(in.HookEventName, out.String())
}

// deliverChannel appends any wake-worthy records since m's read frontier to
// out, advancing the frontier past everything consumed. It also carries the
// peer's presence duties: each fire is a heartbeat (a session making tool
// calls is alive by definition — the fix for issue #58's false departures),
// and, wake permitting, a reap pass so an all-hook channel still retires dead
// peers.
func deliverChannel(ctx context.Context, out *strings.Builder, c *channel.Channel, m membership, reapOK bool) {
	if created, err := c.EnsurePresence(m.Name); err == nil && created {
		_ = c.Append(ctx, channel.Record{
			Sender: m.Name,
			Cwd:    agentCwd(),
			Branch: agentBranch(),
			Kind:   "join",
			Body:   channel.RejoinBody,
		})
	}
	if reapOK {
		c.ReapStale(m.Name)
	}

	off, ok := c.ReadOffset(m.Name)
	if !ok {
		// No frontier (joined before frontier seeding existed, or it was
		// cleared): start from now — history is for catch-up, not wakes.
		end, err := c.End()
		if err != nil {
			return
		}
		_ = c.SaveReadOffset(m.Name, end.Offset())
		return
	}
	recs, next, err := c.ReadSince(ctx, channel.CursorAt(off))
	if err != nil {
		return
	}
	var worthy []channel.Record
	for _, r := range recs {
		if hookWorthy(r, m.Name) {
			worthy = append(worthy, r)
		}
	}
	if len(worthy) > 0 {
		fmt.Fprintf(out, "[agent-chat] New on channel %q (you are %q):\n", c.Slug(), m.Name)
		for i, r := range worthy {
			// Budget the injection: past maxHookRecords the rest stays in the
			// log, recoverable by the named history command — never silently.
			if i == maxHookRecords {
				fmt.Fprintf(out, "…(agent-chat: %d more — run: %s history %q --since %q)\n",
					len(worthy)-i, selfInvocation(), c.Slug(), r.Ts)
				break
			}
			emitStreamRecord(out, r, c.Slug())
		}
	}
	_ = c.SaveReadOffset(m.Name, next.Offset())
}

// maxHookRecords caps how many records one hook fire injects as context. A
// burst beyond it costs the agent a history call instead of a context flood.
const maxHookRecords = 8

// hookWorthy is the stream path's wake filter (skip self, FYI, and directed
// records naming someone else) plus stateless flap suppression: the hook has
// no resident debouncer to hold a timed-out [leave] against a reconnect (see
// flapDebouncer), so it drops both halves of a possible flap outright. A
// genuinely departed peer is still visible — its directed traffic bounces
// (ADR-0011) and bounces ARE delivered — and presence is always inspectable
// via history; what's lost is only the unsolicited departure notice.
func hookWorthy(r channel.Record, me string) bool {
	if r.Sender == me || r.Kind == channel.KindFYI {
		return false
	}
	if (r.Kind == "msg" || r.Kind == channel.KindBounce) && len(r.Mentions) > 0 && !slices.Contains(r.Mentions, me) {
		return false
	}
	if r.Kind == "leave" && r.Body == channel.LeaveBodyTimedOut {
		return false
	}
	if r.Kind == "join" && r.Body == channel.RejoinBody {
		return false
	}
	return true
}

// sessionEndLeave is the clean departure a hook-subscribed session gets for
// free: the same leave/presence/frontier teardown stream and wait perform on
// signal. SessionEnd hooks share a ~1.5s budget, so each channel gets one
// short-fused attempt and failures are abandoned — the reaper will finish the
// job as a timed-out leave.
func sessionEndLeave(root string, ms []membership) {
	for _, m := range ms {
		c, err := channel.Open(root, m.Slug)
		if err != nil || !c.Exists() {
			continue
		}
		_ = c.Leave(m.Name, "left channel")
		_ = c.RemovePresence(m.Name)
		_ = c.ClearReadOffset(m.Name)
	}
}

type hookInput struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Cwd           string `json:"cwd"`
}

// readHookInput parses the harness's stdin JSON, tolerating absence or garbage
// (a manual invocation, a future schema) by returning the zero value.
func readHookInput(r io.Reader) hookInput {
	var in hookInput
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err == nil {
		_ = json.Unmarshal(data, &in)
	}
	return in
}

// emitHookOutput wraps context in the hook protocol's JSON envelope. The event
// name must echo the invoking event or the harness discards the output; when
// invoked outside a known injecting event (manual testing), default to
// PostToolUse so the payload is at least visible.
func emitHookOutput(event, context string) {
	if event != "PostToolUse" && event != "UserPromptSubmit" {
		event = "PostToolUse"
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": context,
		},
	})
}

// selfInvocation names this binary the way the receiving agent can re-run it,
// preferring the stable installed path over a bare name.
func selfInvocation() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "agent-chat"
}
