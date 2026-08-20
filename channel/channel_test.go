package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testChannel(t *testing.T) *Channel {
	t.Helper()
	c, err := Open(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ── mention extraction ───────────────────────────────────────────────────────

func TestExtractMentions(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"hello @alice", []string{"alice"}},
		{"@bob and @alice", []string{"alice", "bob"}}, // sorted, matching jq unique
		// scoped package: @vercel/otel → extracts "vercel" (slash stops scan)
		{"check @vercel/otel please", []string{"vercel"}},
		// no preceding space, but preceded by a non-ident: punctuation
		{"ping (@alice)", []string{"alice"}},
		// embedded: not a mention (preceded by ident char)
		{"user@example.com", nil},
		// token too long (41 chars)
		{"@" + string(make([]byte, 41)), nil},
		// multi-mention deduplicated
		{"@alice @alice again", []string{"alice"}},
		// dash/underscore in name
		{"@alice-bot @bob_bot", []string{"alice-bot", "bob_bot"}},

		// issue #57: an @token inside a code span is quoted content, not an address.
		{"run `@dependabot rebase`", nil},
		{"see `@vercel/otel` for the shape", nil},
		{"```\n@dependabot rebase\n```", nil},
		{"``a `@alice` b``", nil},              // nested: outer run of 2 spans the whole thing
		{"`@alice` but @bob", []string{"bob"}}, // quoting is per-token, not per-message
		// An unterminated run is literal text and must not silence what follows,
		// or a stray backtick would quietly swallow every address after it.
		{"stray ` then @alice", []string{"alice"}},
		{"`@alice", []string{"alice"}},
		// Two spans in one body, each quoting independently.
		{"`@alice` and `@bob` are both quoted", nil},
		// Greedy left-to-right pairing: a stray backtick pairs with the OPENING
		// delimiter of a later span, leaving that token unquoted. Hit live while
		// testing the fix. This is the safe direction — the token resurfaces as a
		// mention (refused loudly) rather than being masked (silent under-delivery)
		// — but it must stay pinned, because the unsafe direction is one greedy-vs-
		// lazy change away.
		{"stray ` then `@alice` quoted", []string{"alice"}},
	}
	for _, tc := range cases {
		got := ExtractMentions(tc.body)
		if len(got) != len(tc.want) {
			t.Errorf("ExtractMentions(%q): got %v, want %v", tc.body, got, tc.want)
			continue
		}
		for i, g := range got {
			if g != tc.want[i] {
				t.Errorf("ExtractMentions(%q)[%d]: got %q, want %q", tc.body, i, g, tc.want[i])
			}
		}
	}
}

func TestFilterMentions(t *testing.T) {
	members := []string{"alice", "bob"}
	got := FilterMentions([]string{"alice", "vercel", "bob"}, members)
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Errorf("FilterMentions: got %v, want [alice bob]", got)
	}
	// unknown-only → empty (broadcast)
	got = FilterMentions([]string{"vercel"}, members)
	if len(got) != 0 {
		t.Errorf("FilterMentions unrecognized: got %v, want []", got)
	}
}

// ── lock acquire / release ───────────────────────────────────────────────────

func TestAcquireReleaseLock(t *testing.T) {
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f, err := c.acquireLock(ctx)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	// Second acquire from a different Channel value should block then time out.
	c2, _ := Open(c.root, c.slug)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if _, err = c2.acquireLock(ctx2); err == nil {
		t.Fatal("expected timeout on second acquire, got nil")
	}
	releaseLock(f)
	// Now it should succeed.
	ctx3, cancel3 := context.WithTimeout(context.Background(), time.Second)
	defer cancel3()
	f2, err := c2.acquireLock(ctx3)
	if err != nil {
		t.Fatalf("acquireLock after release: %v", err)
	}
	releaseLock(f2)
}

// ── record append / read ─────────────────────────────────────────────────────

func TestAppendReadRecord(t *testing.T) {
	c := testChannel(t)

	want := Record{
		Ts:       "2026-01-01T00:00:00Z",
		Sender:   "alice",
		Cwd:      "/tmp",
		Branch:   "main",
		Kind:     "msg",
		Body:     "hello\nworld",
		Mentions: []string{"bob"},
	}
	if err := c.Append(context.Background(), want); err != nil {
		t.Fatal(err)
	}

	records, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	got := records[0]
	if got.Sender != want.Sender || got.Body != want.Body || got.Kind != want.Kind {
		t.Errorf("record mismatch: got %+v, want %+v", got, want)
	}
}

func TestReadMissingChannel(t *testing.T) {
	c := testChannel(t)
	if _, err := c.Read(); err == nil {
		t.Error("expected error reading a channel that was never written")
	}
}

func TestAppendStampsTs(t *testing.T) {
	c := testChannel(t)
	if err := c.Append(context.Background(), Record{Sender: "alice", Kind: "msg", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	records, _ := c.Read()
	if len(records) != 1 || records[0].Ts == "" {
		t.Errorf("expected Append to stamp Ts, got %+v", records)
	}
}

// ── cursor / ReadSince ───────────────────────────────────────────────────────

func TestReadSinceCursor(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()

	// From the start, an empty channel yields nothing.
	recs, cur, err := c.ReadSince(ctx, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("empty channel: got %d records", len(recs))
	}

	_ = c.Append(ctx, Record{Ts: "t1", Sender: "alice", Kind: "msg", Body: "one"})
	_ = c.Append(ctx, Record{Ts: "t2", Sender: "bob", Kind: "msg", Body: "two"})

	recs, cur, err = c.ReadSince(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Body != "one" || recs[1].Body != "two" {
		t.Fatalf("first poll: got %+v", recs)
	}

	// A second poll from the returned cursor sees only new records — no
	// re-read of the two we already consumed.
	_ = c.Append(ctx, Record{Ts: "t3", Sender: "alice", Kind: "msg", Body: "three"})
	recs, cur, err = c.ReadSince(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Body != "three" {
		t.Fatalf("second poll: got %+v", recs)
	}

	// Polling again with no new writes yields nothing and a stable cursor.
	recs, cur2, err := c.ReadSince(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 || cur2 != cur {
		t.Fatalf("idle poll: got %d records, cursor %v → %v", len(recs), cur, cur2)
	}
}

// A long-lived consumer persists the read position as a bare int64 and restores
// it across a restart, surfacing each record exactly once.
func TestCursorOffsetRoundTrip(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()

	_ = c.Append(ctx, Record{Ts: "t1", Sender: "alice", Kind: "msg", Body: "one"})
	_ = c.Append(ctx, Record{Ts: "t2", Sender: "bob", Kind: "msg", Body: "two"})

	recs, cur, err := c.ReadSince(ctx, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("first poll: got %d records, want 2", len(recs))
	}

	// Persist the position, then reconstruct it — must equal the original cursor.
	saved := cur.Offset()
	if saved <= 0 {
		t.Fatalf("Offset() = %d, want > 0 after reading records", saved)
	}
	if got := CursorAt(saved); got != cur {
		t.Fatalf("CursorAt(Offset()) = %v, want %v", got, cur)
	}

	// Simulate a restart: rebuild the cursor purely from the saved int64 and
	// confirm ReadSince surfaces only records written after it.
	_ = c.Append(ctx, Record{Ts: "t3", Sender: "alice", Kind: "msg", Body: "three"})
	recs, _, err = c.ReadSince(ctx, CursorAt(saved))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Body != "three" {
		t.Fatalf("resume poll: got %+v, want only 'three'", recs)
	}

	// A persisted offset past a shrunken log (channel recreated under the same
	// slug) self-heals to the start rather than seeking into garbage.
	recs, _, err = c.ReadSince(ctx, CursorAt(saved+1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("stale-offset self-heal: got %d records, want all 3 from start", len(recs))
	}
}

func TestEndCursorSkipsHistory(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()
	_ = c.Append(ctx, Record{Ts: "t1", Sender: "alice", Kind: "msg", Body: "old"})

	// A peer that starts at End() ignores the pre-existing record.
	cur, err := c.End()
	if err != nil {
		t.Fatal(err)
	}
	recs, cur, err := c.ReadSince(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("End() should skip history, got %+v", recs)
	}

	_ = c.Append(ctx, Record{Ts: "t2", Sender: "bob", Kind: "msg", Body: "new"})
	recs, _, err = c.ReadSince(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Body != "new" {
		t.Fatalf("expected only the post-End record, got %+v", recs)
	}
}

func TestReadSinceShrinkResets(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()
	_ = c.Append(ctx, Record{Ts: "t1", Sender: "alice", Kind: "msg", Body: "before"})
	_, cur, _ := c.ReadSince(ctx, Cursor{})

	// Simulate the channel being deleted and recreated under the same slug:
	// the log is now shorter than the cursor.
	if err := os.Remove(c.logPath()); err != nil {
		t.Fatal(err)
	}
	_ = c.Append(ctx, Record{Ts: "t2", Sender: "bob", Kind: "msg", Body: "after"})

	recs, _, err := c.ReadSince(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Body != "after" {
		t.Fatalf("shrink should reset cursor to start, got %+v", recs)
	}
}

func TestReadSincePartialLine(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()
	_ = c.Append(ctx, Record{Ts: "t1", Sender: "alice", Kind: "msg", Body: "whole"})

	// Append a partial line (no trailing newline) as a concurrent writer mid-write would.
	f, _ := os.OpenFile(c.logPath(), os.O_APPEND|os.O_WRONLY, 0644)
	_, _ = f.WriteString(`{"ts":"t2","sender":"bob","kind":"msg","body":"partial"`)
	_ = f.Close()

	recs, cur, err := c.ReadSince(ctx, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Body != "whole" {
		t.Fatalf("partial line should not be consumed, got %+v", recs)
	}

	// Completing the line makes it visible on the next poll.
	f, _ = os.OpenFile(c.logPath(), os.O_APPEND|os.O_WRONLY, 0644)
	_, _ = f.WriteString("}\n")
	_ = f.Close()
	recs, _, err = c.ReadSince(ctx, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Body != "partial" {
		t.Fatalf("completed line should now be read, got %+v", recs)
	}
}

// ── JSON schema ──────────────────────────────────────────────────────────────

func TestRecordJSONSchema(t *testing.T) {
	r := Record{
		Ts:     "2026-01-01T00:00:00Z",
		Sender: "alice",
		Cwd:    "/repo",
		Branch: "main",
		Kind:   "msg",
		Body:   "hi",
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["mentions"]; ok {
		t.Errorf("expected mentions key absent when empty, got: %s", b)
	}
	r.Mentions = []string{"bob"}
	b2, _ := json.Marshal(r)
	var m2 map[string]any
	_ = json.Unmarshal(b2, &m2)
	if _, ok := m2["mentions"]; !ok {
		t.Error("expected mentions key present when non-empty")
	}
}

// TestRecordJSONGolden pins the exact on-disk bytes: field order, omitempty,
// no HTML escaping, trailing newline. This is the published schema — if this
// test changes, the wire format changed and it is a breaking (major) change.
func TestRecordJSONGolden(t *testing.T) {
	r := Record{
		Ts:       "2026-01-01T00:00:00Z",
		Sender:   "alice",
		Cwd:      "/repo",
		Branch:   "main",
		Kind:     "msg",
		Body:     "hi <b>&", // < > & must NOT be \u-escaped on disk
		Mentions: []string{"bob"},
	}
	want := `{"ts":"2026-01-01T00:00:00Z","sender":"alice","cwd":"/repo","branch":"main","kind":"msg","body":"hi <b>&","mentions":["bob"]}` + "\n"

	// Assert the marshaler the package itself uses to write the log.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	if buf.String() != want {
		t.Errorf("schema drift:\n got: %s\nwant: %s", buf.String(), want)
	}

	// Assert the actual write path (Append) lays down those same bytes.
	c := testChannel(t)
	if err := c.Append(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(c.logPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Errorf("on-disk drift:\n got: %s\nwant: %s", raw, want)
	}
}

// ── stale-peer reaping ───────────────────────────────────────────────────────

func TestReapStalePeers(t *testing.T) {
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}

	// Plant a ghost with a backdated presence file.
	presDir := c.presDir()
	_ = os.MkdirAll(presDir, 0755)
	ghost := filepath.Join(presDir, "ghost")
	if f, err := os.Create(ghost); err == nil {
		_ = f.Close()
	}
	stale := time.Now().Add(-10 * time.Minute)
	_ = os.Chtimes(ghost, stale, stale)

	t.Setenv("AGENT_CHAT_STALE_SECS", "30")

	c.ReapStale("live")

	if _, err := os.Stat(ghost); !os.IsNotExist(err) {
		t.Error("expected ghost presence file to be removed after reap")
	}

	records, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range records {
		if r.Sender == "ghost" && r.Kind == "leave" {
			found = true
		}
	}
	if !found {
		t.Error("expected a leave record for ghost after reap")
	}

	// Reaping twice must not double-post.
	c.ReapStale("live")
	records2, _ := c.Read()
	var count int
	for _, r := range records2 {
		if r.Sender == "ghost" && r.Kind == "leave" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 leave for ghost, got %d", count)
	}
}

// ── active members (freshness) ────────────────────────────────────────────────

func TestActiveMembers(t *testing.T) {
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")

	// Fresh peer (touched just now).
	if err := c.TouchPresence("live"); err != nil {
		t.Fatal(err)
	}
	// Stale peer (heartbeat backdated past the cutoff).
	if err := c.TouchPresence("ghost"); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(c.presFile("ghost"), stale, stale); err != nil {
		t.Fatal(err)
	}

	active := c.ActiveMembers()
	if len(active) != 1 || active[0] != "live" {
		t.Errorf("expected [live] active members, got %v", active)
	}

	// Members (no freshness filter) still sees both — ActiveMembers is read-only
	// and must not have reaped the ghost.
	if got := len(c.Members()); got != 2 {
		t.Errorf("expected Members to still list both peers, got %d", got)
	}
}

// ── touch / remove presence ──────────────────────────────────────────────────

func TestPresence(t *testing.T) {
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}

	if err := c.TouchPresence("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.presFile("alice")); err != nil {
		t.Errorf("presence file should exist after touch: %v", err)
	}
	if members := c.Members(); len(members) != 1 || members[0] != "alice" {
		t.Errorf("expected [alice] members, got %v", members)
	}

	if err := c.RemovePresence("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.presFile("alice")); !os.IsNotExist(err) {
		t.Error("presence file should be gone after remove")
	}
}

// RefreshPresence bumps an existing heartbeat but never creates one — a reaped
// peer stays gone until it re-registers (issue #29 defect 1).
func TestRefreshPresenceDoesNotResurrect(t *testing.T) {
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}

	// No presence file yet: refresh is a no-op, not a create.
	if err := c.RefreshPresence("ghost"); err != nil {
		t.Fatalf("RefreshPresence on a missing peer should be a no-op: %v", err)
	}
	if _, err := os.Stat(c.presFile("ghost")); !os.IsNotExist(err) {
		t.Fatal("RefreshPresence must not create a presence file")
	}

	// Once registered, refresh bumps the (backdated) mtime forward.
	if err := c.TouchPresence("worker"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(c.presFile("worker"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshPresence("worker"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(c.presFile("worker"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().After(old.Add(time.Minute)) {
		t.Error("RefreshPresence did not bump the mtime of a registered peer")
	}
}

// After a peer is reaped, a heartbeat must not resurrect it, so the sweep does
// not re-announce the same departure with no intervening join (issue #29
// defect 1). This is the regression test for the ~11× repeated [leave].
func TestReapedPeerNotReReapedByHeartbeat(t *testing.T) {
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")

	// A peer registers, then goes stale and is reaped once.
	if err := c.TouchPresence("ghost"); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(c.presFile("ghost"), stale, stale); err != nil {
		t.Fatal(err)
	}
	c.ReapStale("live")

	// RefreshPresence is the "never resurrect" primitive (send.go drives it): a
	// bare refresh of a reaped peer must not re-create the file...
	if err := c.RefreshPresence("ghost"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.presFile("ghost")); !os.IsNotExist(err) {
		t.Fatal("a heartbeat resurrected a reaped peer's presence file")
	}
	// ...so a second sweep finds nothing to reap and posts no further leave.
	c.ReapStale("live")

	records, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	var leaves int
	for _, r := range records {
		if r.Sender == "ghost" && r.Kind == "leave" {
			leaves++
		}
	}
	if leaves != 1 {
		t.Errorf("expected exactly 1 leave for a reaped peer, got %d", leaves)
	}
}

// EnsurePresence refreshes an existing file and reports created == false, but
// re-creates a reaped peer's file and reports created == true — the signal a
// caller uses to re-announce a [join].
func TestEnsurePresenceSelfHeals(t *testing.T) {
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}

	// First call on a fresh peer creates the file.
	created, err := c.EnsurePresence("worker")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("EnsurePresence on a missing peer should report created == true")
	}

	// A second call refreshes (does not re-create) and bumps the mtime forward.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(c.presFile("worker"), old, old); err != nil {
		t.Fatal(err)
	}
	created, err = c.EnsurePresence("worker")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("EnsurePresence on an existing peer should report created == false")
	}
	info, err := os.Stat(c.presFile("worker"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().After(old.Add(time.Minute)) {
		t.Error("EnsurePresence did not refresh the mtime of an existing peer")
	}
}

// ── join: race-free name claim ───────────────────────────────────────────────

func TestJoinAssignsRequestedName(t *testing.T) {
	c := testChannel(t)
	got, err := c.Join(context.Background(), Record{Sender: "worker", Kind: "join", Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "worker" {
		t.Errorf("expected assigned name %q, got %q", "worker", got)
	}
	// The name is claimed (presence file) and the join record carries it.
	if _, err := os.Stat(c.presFile("worker")); err != nil {
		t.Errorf("join should claim presence: %v", err)
	}
	recs, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Sender != "worker" || recs[0].Kind != "join" {
		t.Errorf("expected one join record from worker, got %+v", recs)
	}
}

func TestJoinRejectsActiveCollision(t *testing.T) {
	c := testChannel(t)
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")
	// First worker takes the name.
	if _, err := c.Join(context.Background(), Record{Sender: "worker", Kind: "join", Body: "a"}); err != nil {
		t.Fatal(err)
	}
	// Second worker requests the same name while the first is still active: it is
	// rejected with ErrNameTaken, and nothing is written for it.
	_, err := c.Join(context.Background(), Record{Sender: "worker", Kind: "join", Body: "b"})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("expected ErrNameTaken, got %v", err)
	}
	recs, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Errorf("rejected join must not append a record; log has %d records", len(recs))
	}
}

func TestJoinReusesStaleName(t *testing.T) {
	c := testChannel(t)
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")
	// A name held only by a timed-out (e.g. SIGKILLed) peer must be reusable, so
	// ghosts don't permanently burn names.
	if _, err := c.Join(context.Background(), Record{Sender: "worker", Kind: "join", Body: "a"}); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(c.presFile("worker"), stale, stale); err != nil {
		t.Fatal(err)
	}
	got, err := c.Join(context.Background(), Record{Sender: "worker", Kind: "join", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "worker" {
		t.Errorf("stale name should be reusable; expected %q, got %q", "worker", got)
	}
}
