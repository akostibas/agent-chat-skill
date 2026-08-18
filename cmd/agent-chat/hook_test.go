package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akostibas/agent-chat-skill/channel"
)

// TestHookWorthy pins the hook's wake filter: the stream's rules (skip self,
// FYI, directed-elsewhere) plus stateless flap suppression (both halves of a
// reap/reconnect pair are dropped outright — the hook has no debouncer to
// arbitrate them).
func TestHookWorthy(t *testing.T) {
	cases := []struct {
		name string
		r    channel.Record
		want bool
	}{
		{"own message", channel.Record{Sender: "me", Kind: "msg", Body: "x"}, false},
		{"fyi", channel.Record{Sender: "peer", Kind: channel.KindFYI, Body: "x"}, false},
		{"broadcast msg", channel.Record{Sender: "peer", Kind: "msg", Body: "x"}, true},
		{"directed to me", channel.Record{Sender: "peer", Kind: "msg", Body: "x", Mentions: []string{"me"}}, true},
		{"directed elsewhere", channel.Record{Sender: "peer", Kind: "msg", Body: "x", Mentions: []string{"other"}}, false},
		{"bounce to me", channel.Record{Sender: "peer", Kind: channel.KindBounce, Body: "x", Mentions: []string{"me"}}, true},
		{"bounce elsewhere", channel.Record{Sender: "peer", Kind: channel.KindBounce, Body: "x", Mentions: []string{"other"}}, false},
		{"clean leave", channel.Record{Sender: "peer", Kind: "leave", Body: "left channel"}, true},
		{"timed-out leave (flap half)", channel.Record{Sender: "peer", Kind: "leave", Body: channel.LeaveBodyTimedOut}, false},
		{"plain join", channel.Record{Sender: "peer", Kind: "join", Body: "joined channel"}, true},
		{"reconnect join (flap half)", channel.Record{Sender: "peer", Kind: "join", Body: channel.RejoinBody}, false},
	}
	for _, tc := range cases {
		if got := hookWorthy(tc.r, "me"); got != tc.want {
			t.Errorf("%s: hookWorthy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSessionRegistry(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-123")

	registerSession(root, "alpha", "amy")
	registerSession(root, "beta", "bob")
	path := sessionFile(root, "sid-123")
	if got := readMemberships(path); len(got) != 2 {
		t.Fatalf("want 2 memberships, got %v", got)
	}

	// Re-join of the same slug replaces the entry rather than duplicating it.
	registerSession(root, "alpha", "amy2")
	ms := readMemberships(path)
	if len(ms) != 2 {
		t.Fatalf("re-join duplicated: %v", ms)
	}
	var alphaName string
	for _, m := range ms {
		if m.Slug == "alpha" {
			alphaName = m.Name
		}
	}
	if alphaName != "amy2" {
		t.Errorf("re-join kept old name: %v", ms)
	}

	deregisterSession(root, "alpha")
	if ms := readMemberships(path); len(ms) != 1 || ms[0].Slug != "beta" {
		t.Fatalf("after deregister want [beta], got %v", ms)
	}
	// Dropping the last membership removes the file — the hook's fast path is
	// a stat, so an empty file would keep taxing this session forever.
	deregisterSession(root, "beta")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("registry file should be removed with its last membership (err=%v)", err)
	}
}

func TestSessionRegistryRejectsUnsafeID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "../../escape")
	registerSession(root, "alpha", "amy")
	if matches, _ := filepath.Glob(filepath.Join(root, "sessions", "*")); len(matches) != 0 {
		t.Fatalf("unsafe session id produced registry entries: %v", matches)
	}
}

func newTestChannel(t *testing.T) (*channel.Channel, string) {
	t.Helper()
	root := t.TempDir()
	c, err := channel.Open(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c, root
}

func TestDeliverChannelExactlyOnce(t *testing.T) {
	c, _ := newTestChannel(t)
	ctx := context.Background()
	if _, err := c.Join(ctx, channel.Record{Sender: "me", Kind: "join", Body: "joined channel"}); err != nil {
		t.Fatal(err)
	}
	end, _ := c.End()
	_ = c.SaveReadOffset("me", end.Offset())

	if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "hello me"}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	deliverChannel(ctx, &out, c, membership{Slug: "test", Name: "me"}, true)
	if !strings.Contains(out.String(), "hello me") {
		t.Fatalf("first fire should deliver the message, got: %q", out.String())
	}
	out.Reset()
	deliverChannel(ctx, &out, c, membership{Slug: "test", Name: "me"}, true)
	if out.Len() != 0 {
		t.Fatalf("second fire must deliver nothing (frontier advanced), got: %q", out.String())
	}
}

// A membership with no persisted frontier (pre-registry join, or a cleared
// cursor) seeds at the log end and delivers nothing: history is for catch-up,
// not wakes.
func TestDeliverChannelSeedsWithoutFrontier(t *testing.T) {
	c, _ := newTestChannel(t)
	ctx := context.Background()
	if _, err := c.Join(ctx, channel.Record{Sender: "me", Kind: "join", Body: "joined channel"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "before subscribe"}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	deliverChannel(ctx, &out, c, membership{Slug: "test", Name: "me"}, true)
	if out.Len() != 0 {
		t.Fatalf("seeding fire must not replay history, got: %q", out.String())
	}
	if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "after subscribe"}); err != nil {
		t.Fatal(err)
	}
	deliverChannel(ctx, &out, c, membership{Slug: "test", Name: "me"}, true)
	if !strings.Contains(out.String(), "after subscribe") || strings.Contains(out.String(), "before subscribe") {
		t.Fatalf("want only post-seed records, got: %q", out.String())
	}
}

func TestDeliverChannelCapsBurst(t *testing.T) {
	c, _ := newTestChannel(t)
	ctx := context.Background()
	if _, err := c.Join(ctx, channel.Record{Sender: "me", Kind: "join", Body: "joined channel"}); err != nil {
		t.Fatal(err)
	}
	end, _ := c.End()
	_ = c.SaveReadOffset("me", end.Offset())
	for range maxHookRecords + 3 {
		if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "spam"}); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	deliverChannel(ctx, &out, c, membership{Slug: "test", Name: "me"}, true)
	if got := strings.Count(out.String(), "[20"); got > maxHookRecords { // record headers carry the ts year
		t.Fatalf("emitted %d records, cap is %d", got, maxHookRecords)
	}
	if !strings.Contains(out.String(), "3 more") {
		t.Fatalf("overflow must name the count and a history recovery, got: %q", out.String())
	}
}

func TestEnsureHookEntry(t *testing.T) {
	hooks := map[string]any{}
	cmd := `BIN="$HOME/x/agent-chat"; [ -x "$BIN" ] || exit 0; ` + hookCmdMarker

	if !ensureHookEntry(hooks, "PostToolUse", cmd) {
		t.Fatal("fresh install should report a change")
	}
	if ensureHookEntry(hooks, "PostToolUse", cmd) {
		t.Fatal("identical re-install must be a no-op")
	}

	// A moved binary updates the existing entry in place — no duplicates.
	moved := `BIN="$HOME/y/agent-chat"; [ -x "$BIN" ] || exit 0; ` + hookCmdMarker
	if !ensureHookEntry(hooks, "PostToolUse", moved) {
		t.Fatal("changed command should report a change")
	}
	groups := hooks["PostToolUse"].([]any)
	if len(groups) != 1 {
		t.Fatalf("want 1 group after update, got %d", len(groups))
	}

	// Foreign hooks on the same event are preserved untouched.
	hooks["PreToolUse"] = []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "other-tool guard"}}},
	}
	ensureHookEntry(hooks, "PreToolUse", cmd)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 2 {
		t.Fatalf("foreign entry lost: %v", pre)
	}
}
