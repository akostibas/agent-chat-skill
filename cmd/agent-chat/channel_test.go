package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── mention extraction ───────────────────────────────────────────────────────

func TestExtractMentions(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"hello @alice", []string{"alice"}},
		{"@bob and @alice", []string{"bob", "alice"}},
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
	}
	for _, tc := range cases {
		got := extractMentions(tc.body)
		if len(got) != len(tc.want) {
			t.Errorf("extractMentions(%q): got %v, want %v", tc.body, got, tc.want)
			continue
		}
		for i, g := range got {
			if g != tc.want[i] {
				t.Errorf("extractMentions(%q)[%d]: got %q, want %q", tc.body, i, g, tc.want[i])
			}
		}
	}
}

func TestFilterMentions(t *testing.T) {
	members := []string{"alice", "bob"}
	got := filterMentions([]string{"alice", "vercel", "bob"}, members)
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Errorf("filterMentions: got %v, want [alice bob]", got)
	}
	// unknown-only → empty (broadcast)
	got = filterMentions([]string{"vercel"}, members)
	if len(got) != 0 {
		t.Errorf("filterMentions unrecognized: got %v, want []", got)
	}
}

// ── lock acquire / release ───────────────────────────────────────────────────

func TestAcquireReleaseLock(t *testing.T) {
	dir := t.TempDir()
	c := &Channel{root: dir, slug: "test"}
	_ = os.MkdirAll(c.dir(), 0755)

	f, err := c.acquireLock(time.Second)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	// Second acquire from a different Channel value should block then timeout.
	c2 := &Channel{root: dir, slug: "test"}
	_, err = c2.acquireLock(100 * time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout on second acquire, got nil")
	}
	releaseLock(f)
	// Now it should succeed.
	f2, err := c2.acquireLock(time.Second)
	if err != nil {
		t.Fatalf("acquireLock after release: %v", err)
	}
	releaseLock(f2)
}

// ── record append / read ─────────────────────────────────────────────────────

func TestAppendReadRecord(t *testing.T) {
	dir := t.TempDir()
	c := &Channel{root: dir, slug: "test"}
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}

	want := Record{
		Ts:       "2026-01-01T00:00:00Z",
		Sender:   "alice",
		Cwd:      "/tmp",
		Branch:   "main",
		Kind:     "msg",
		Body:     "hello\nworld",
		Mentions: []string{"bob"},
	}

	lockF, _ := c.acquireLock(time.Second)
	if err := c.appendRecord(want); err != nil {
		releaseLock(lockF)
		t.Fatal(err)
	}
	releaseLock(lockF)

	records, err := c.readLog()
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
	s := string(b)
	// Mentions omitted when empty (omitempty).
	if _, ok := func() (interface{}, bool) {
		var m map[string]interface{}
		json.Unmarshal(b, &m)
		v, ok := m["mentions"]
		return v, ok
	}(); ok {
		t.Errorf("expected mentions key absent when empty, got: %s", s)
	}
	// With mentions present.
	r.Mentions = []string{"bob"}
	b2, _ := json.Marshal(r)
	var m2 map[string]interface{}
	json.Unmarshal(b2, &m2)
	if _, ok := m2["mentions"]; !ok {
		t.Error("expected mentions key present when non-empty")
	}
}

// ── stale-peer reaping ───────────────────────────────────────────────────────

func TestReapStalePeers(t *testing.T) {
	dir := t.TempDir()
	c := &Channel{root: dir, slug: "test"}
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}

	// Plant a ghost with a backdated presence file.
	presDir := c.presDir()
	_ = os.MkdirAll(presDir, 0755)
	ghost := filepath.Join(presDir, "ghost")
	if f, err := os.Create(ghost); err == nil {
		f.Close()
	}
	stale := time.Now().Add(-10 * time.Minute)
	_ = os.Chtimes(ghost, stale, stale)

	// Lower the staleness threshold so the test doesn't need to wait.
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")

	c.reapStalePeers("live")

	// Presence file must be gone.
	if _, err := os.Stat(ghost); !os.IsNotExist(err) {
		t.Error("expected ghost presence file to be removed after reap")
	}

	// A leave record for "ghost" must be in the log.
	records, err := c.readLog()
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
	c.reapStalePeers("live")
	records2, _ := c.readLog()
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

// ── touch / remove presence ──────────────────────────────────────────────────

func TestPresence(t *testing.T) {
	dir := t.TempDir()
	c := &Channel{root: dir, slug: "test"}
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}

	c.touchPresence("alice")
	if _, err := os.Stat(c.presFile("alice")); err != nil {
		t.Errorf("presence file should exist after touch: %v", err)
	}

	c.removePresence("alice")
	if _, err := os.Stat(c.presFile("alice")); !os.IsNotExist(err) {
		t.Error("presence file should be gone after remove")
	}
}
