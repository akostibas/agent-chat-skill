package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

// The reap race found live in #60's testing: ReapStale clears the cursor
// file, and a resurrected peer must resume from the registry's offset mirror —
// delivering the reap window's traffic late — rather than reseeding at the log
// end and skipping it.
func TestDeliverChannelMirrorRecoversReapWindow(t *testing.T) {
	c, _ := newTestChannel(t)
	ctx := context.Background()
	if _, err := c.Join(ctx, channel.Record{Sender: "me", Kind: "join", Body: "joined channel"}); err != nil {
		t.Fatal(err)
	}
	end, _ := c.End()
	_ = c.SaveReadOffset("me", end.Offset())
	m := &membership{Slug: "test", Name: "me", Offset: end.Offset()}

	var out strings.Builder
	if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "delivered normally"}); err != nil {
		t.Fatal(err)
	}
	deliverChannel(ctx, &out, c, m, true)

	// Reap: cursor cleared, presence gone; then traffic lands in the window.
	_ = c.ClearReadOffset("me")
	_ = c.RemovePresence("me")
	if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "sent during reap window"}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	deliverChannel(ctx, &out, c, m, true)
	if !strings.Contains(out.String(), "sent during reap window") {
		t.Fatalf("mirror recovery must deliver the reap window's traffic, got: %q", out.String())
	}
	if strings.Contains(out.String(), "delivered normally") {
		t.Fatalf("mirror recovery replayed already-delivered traffic: %q", out.String())
	}
}

// A mirror pointing past the log end (channel deleted and recreated under the
// same slug) must fall back to a fresh end-seed, not replay the new channel's
// whole history.
func TestDeliverChannelMirrorPastEndReseeds(t *testing.T) {
	c, _ := newTestChannel(t)
	ctx := context.Background()
	if _, err := c.Join(ctx, channel.Record{Sender: "me", Kind: "join", Body: "joined channel"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "old world"}); err != nil {
		t.Fatal(err)
	}
	m := &membership{Slug: "test", Name: "me", Offset: 1 << 40}
	var out strings.Builder
	deliverChannel(ctx, &out, c, m, true)
	if out.Len() != 0 {
		t.Fatalf("oversized mirror must reseed quietly, got: %q", out.String())
	}
	end, _ := c.End()
	if m.Offset != end.Offset() {
		t.Fatalf("mirror should now sit at the log end (%d), got %d", end.Offset(), m.Offset)
	}
}

func TestDoorbellStateAndNag(t *testing.T) {
	root := t.TempDir()
	m := &membership{Slug: "test", Name: "me"}

	// Never armed: no lockfile, no nag.
	var out strings.Builder
	nagDeadDoorbell(&out, root, m)
	if exists, _ := doorbellState(root, "test", "me"); exists || out.Len() != 0 {
		t.Fatalf("never-armed doorbell must stay silent (exists=%v, out=%q)", exists, out.String())
	}

	// Armed and held: no nag.
	lock := lockDoorbell(root, "test", "me")
	if lock == nil {
		t.Fatal("could not arm doorbell")
	}
	if _, armed := doorbellState(root, "test", "me"); !armed {
		t.Fatal("held doorbell should read as armed")
	}
	nagDeadDoorbell(&out, root, m)
	if out.Len() != 0 {
		t.Fatalf("live doorbell must not nag: %q", out.String())
	}

	// A second doorbell for the same peer parks rather than exiting (#61), then
	// takes over the moment the incumbent lets go.
	dup := make(chan *os.File, 1)
	go func() { dup <- lockDoorbell(root, "test", "me") }()
	select {
	case <-dup:
		t.Fatal("duplicate doorbell returned while one was armed; it must block")
	case <-time.After(200 * time.Millisecond):
	}

	_ = lock.Close()
	select {
	case f := <-dup:
		if f == nil {
			t.Fatal("parked doorbell failed to take over the released lock")
		}
		_ = f.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("parked doorbell never took over the released lock")
	}

	// Dead: lockfile exists, no holder → nag names the re-arm and the opt-out.
	nagDeadDoorbell(&out, root, m)
	if !strings.Contains(out.String(), "--signal") || !strings.Contains(out.String(), "doorbells") {
		t.Fatalf("dead doorbell must nag with re-arm command and lockfile path, got: %q", out.String())
	}
}

// signalLoop wakes on worthy traffic an idle agent hasn't consumed…
func TestSignalLoopWakesWhenIdle(t *testing.T) {
	t.Setenv("AGENT_CHAT_SIGNAL_GRACE_MS", "1")
	c, _ := newTestChannel(t)
	ctx := context.Background()
	if _, err := c.Join(ctx, channel.Record{Sender: "me", Kind: "join", Body: "joined channel"}); err != nil {
		t.Fatal(err)
	}
	end, _ := c.End()
	_ = c.SaveReadOffset("me", end.Offset())

	done := make(chan error, 1)
	go func() { done <- signalLoop(ctx, c, "me") }()
	time.Sleep(150 * time.Millisecond)
	if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "knock"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("signalLoop returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signalLoop did not wake on worthy traffic")
	}
	// The doorbell never touches the frontier — that is the hook's job.
	if off, _ := c.ReadOffset("me"); off != end.Offset() {
		t.Fatalf("signalLoop moved the frontier (%d → %d)", end.Offset(), off)
	}
}

// A doorbell whose peer's presence vanishes (leave.sh or a reap) retires
// itself instead of watching for a departed identity.
func TestSignalLoopRetiresOnMissingPresence(t *testing.T) {
	t.Setenv("AGENT_CHAT_SIGNAL_GRACE_MS", "1")
	c, _ := newTestChannel(t)
	ctx := context.Background()
	if _, err := c.Join(ctx, channel.Record{Sender: "me", Kind: "join", Body: "joined channel"}); err != nil {
		t.Fatal(err)
	}
	end, _ := c.End()
	_ = c.SaveReadOffset("me", end.Offset())

	done := make(chan error, 1)
	go func() { done <- signalLoop(ctx, c, "me") }()
	time.Sleep(150 * time.Millisecond)
	_ = c.RemovePresence("me")
	select {
	case err := <-done:
		if err != errRetired {
			t.Fatalf("want errRetired, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signalLoop did not retire after presence removal")
	}
}

// …but keeps blocking when the hook consumes the traffic during the grace —
// a busy agent's doorbell does not ring.
func TestSignalLoopGraceSkipsConsumed(t *testing.T) {
	t.Setenv("AGENT_CHAT_SIGNAL_GRACE_MS", "500")
	c, _ := newTestChannel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Join(ctx, channel.Record{Sender: "me", Kind: "join", Body: "joined channel"}); err != nil {
		t.Fatal(err)
	}
	end, _ := c.End()
	_ = c.SaveReadOffset("me", end.Offset())

	done := make(chan error, 1)
	go func() { done <- signalLoop(ctx, c, "me") }()
	time.Sleep(150 * time.Millisecond)
	if err := c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "busy knock"}); err != nil {
		t.Fatal(err)
	}
	// Simulate the hook consuming it mid-grace.
	time.Sleep(100 * time.Millisecond)
	newEnd, _ := c.End()
	_ = c.SaveReadOffset("me", newEnd.Offset())

	select {
	case err := <-done:
		t.Fatalf("doorbell rang despite hook consumption (err=%v)", err)
	case <-time.After(1200 * time.Millisecond):
		// still blocking: correct
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("want context.Canceled after cancel, got %v", err)
	}
}
