package channel

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// primeBounceChannel registers a live coordinator and worker, appends the given
// records, and returns the channel ready for the caller to stale + reap the
// worker. coordinator stays a live member so it can be a bounce recipient.
func primeBounceChannel(t *testing.T, records ...Record) *Channel {
	t.Helper()
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}
	if err := c.TouchPresence("coordinator"); err != nil {
		t.Fatal(err)
	}
	if err := c.TouchPresence("worker"); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if err := c.Append(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func bounces(t *testing.T, c *Channel) []Record {
	t.Helper()
	recs, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, r := range recs {
		if r.Kind == KindBounce {
			out = append(out, r)
		}
	}
	return out
}

func makeStale(t *testing.T, c *Channel, name string) {
	t.Helper()
	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(c.presFile(name), stale, stale); err != nil {
		t.Fatal(err)
	}
}

// TestBounceUndeliveredDirectedMessage is the core case: a coordinator addresses
// a worker that dies without reading it; reaping the worker bounces the message
// back to the coordinator, naming the worker and echoing the message.
func TestBounceUndeliveredDirectedMessage(t *testing.T) {
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")
	c := primeBounceChannel(t, Record{
		Sender: "coordinator", Kind: "msg", Body: "please build the migration", Mentions: []string{"worker"},
	})
	makeStale(t, c, "worker")

	c.ReapStale("coordinator")

	bs := bounces(t, c)
	if len(bs) != 1 {
		t.Fatalf("expected exactly 1 bounce, got %d: %+v", len(bs), bs)
	}
	b := bs[0]
	if b.Sender != "worker" {
		t.Errorf("bounce Sender = %q, want %q (the departed peer)", b.Sender, "worker")
	}
	if !slices.Contains(b.Mentions, "coordinator") {
		t.Errorf("bounce Mentions = %v, want to include the original sender %q", b.Mentions, "coordinator")
	}
	if !strings.Contains(b.Body, "worker") || !strings.Contains(b.Body, "please build the migration") {
		t.Errorf("bounce Body = %q, want it to name the departed peer and echo the message", b.Body)
	}
}

// TestReadMessageDoesNotBounce: a message the worker's read frontier has moved
// past was delivered, so it must not bounce.
func TestReadMessageDoesNotBounce(t *testing.T) {
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")
	c := primeBounceChannel(t, Record{
		Sender: "coordinator", Kind: "msg", Body: "you got this one", Mentions: []string{"worker"},
	})
	// Worker read to the current end before dying.
	end, err := c.End()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SaveReadOffset("worker", end.Offset()); err != nil {
		t.Fatal(err)
	}
	makeStale(t, c, "worker")

	c.ReapStale("coordinator")

	if bs := bounces(t, c); len(bs) != 0 {
		t.Errorf("expected no bounce for a read message, got %d: %+v", len(bs), bs)
	}
}

// TestBroadcastDoesNotBounce: an @all message (no mentions) is fire-and-forget
// and must not bounce when a recipient departs.
func TestBroadcastDoesNotBounce(t *testing.T) {
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")
	c := primeBounceChannel(t, Record{
		Sender: "coordinator", Kind: "msg", Body: "status check everyone", Mentions: nil,
	})
	makeStale(t, c, "worker")

	c.ReapStale("coordinator")

	if bs := bounces(t, c); len(bs) != 0 {
		t.Errorf("expected no bounce for a broadcast, got %d: %+v", len(bs), bs)
	}
}

// TestBounceSkippedWhenSenderDeparted: if the original sender is no longer a
// member, the bounce has nowhere to land and is dropped — no bounce into the
// void, and (since a bounce's sender is the departed peer) no cascade.
func TestBounceSkippedWhenSenderDeparted(t *testing.T) {
	t.Setenv("AGENT_CHAT_STALE_SECS", "30")
	c := testChannel(t)
	if err := c.ensureDir(); err != nil {
		t.Fatal(err)
	}
	// Only the worker is ever a member; the sender "ghost-sender" never joined.
	if err := c.TouchPresence("worker"); err != nil {
		t.Fatal(err)
	}
	if err := c.Append(context.Background(), Record{
		Sender: "ghost-sender", Kind: "msg", Body: "unreachable", Mentions: []string{"worker"},
	}); err != nil {
		t.Fatal(err)
	}
	makeStale(t, c, "worker")

	c.ReapStale("live")

	if bs := bounces(t, c); len(bs) != 0 {
		t.Errorf("expected no bounce when sender is not a member, got %d: %+v", len(bs), bs)
	}
}

// TestReadOffsetRoundTrip covers the persistence primitive: save/read/clear, and
// the conservative default of 0 for missing or garbage frontiers.
func TestReadOffsetRoundTrip(t *testing.T) {
	c := testChannel(t)
	if got := c.readOffset("nobody"); got != 0 {
		t.Errorf("readOffset with no file = %d, want 0", got)
	}
	if err := c.SaveReadOffset("worker", 4096); err != nil {
		t.Fatal(err)
	}
	if got := c.readOffset("worker"); got != 4096 {
		t.Errorf("readOffset after save = %d, want 4096", got)
	}
	if err := c.ClearReadOffset("worker"); err != nil {
		t.Fatal(err)
	}
	if got := c.readOffset("worker"); got != 0 {
		t.Errorf("readOffset after clear = %d, want 0", got)
	}
	// Garbage content falls back to 0 rather than erroring.
	if err := c.SaveReadOffset("worker", 10); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.cursorFile("worker"), []byte("not-a-number"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := c.readOffset("worker"); got != 0 {
		t.Errorf("readOffset with garbage = %d, want 0", got)
	}
}
