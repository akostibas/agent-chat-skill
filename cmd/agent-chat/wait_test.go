package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

// captureStdout runs f with os.Stdout redirected to a pipe and returns what it
// wrote. tailAndEmit writes to os.Stdout directly, so tests intercept it here.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	f()
	_ = w.Close()
	out := make([]byte, 64<<10)
	n, _ := r.Read(out)
	return string(out[:n])
}

// One-shot mode returns as soon as a wake-worthy record lands, emits it, and
// leaves the read frontier past it.
func TestTailAndEmitOneShotExitsOnMessage(t *testing.T) {
	c, err := channel.Open(t.TempDir(), "w")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed, _ := c.End()

	done := make(chan string, 1)
	go func() {
		done <- captureStdout(t, func() {
			_ = tailAndEmit(ctx, c, "w", "me", seed, true)
		})
	}()

	time.Sleep(50 * time.Millisecond)
	_ = c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "hello"})

	select {
	case out := <-done:
		if !strings.Contains(out, "hello") {
			t.Errorf("wake output missing message body: %q", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("one-shot tailAndEmit did not exit after a message")
	}

	off, ok := c.ReadOffset("me")
	if !ok {
		t.Fatal("read frontier not persisted")
	}
	end, _ := c.End()
	if off != end.Offset() {
		t.Errorf("frontier %d, want log end %d", off, end.Offset())
	}
}

// A record landing between one wait's exit and the next's arm is delivered by
// the next wait: seeding from the persisted frontier covers the gap.
func TestWaitFrontierCoversReArmGap(t *testing.T) {
	c, err := channel.Open(t.TempDir(), "w")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	end, _ := c.End()
	_ = c.SaveReadOffset("me", end.Offset()) // a previous wait's frontier

	// Message arrives while no wait is running.
	_ = c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Body: "missed-me"})

	// Next wait seeds from the frontier and must deliver it immediately.
	off, ok := c.ReadOffset("me")
	if !ok {
		t.Fatal("expected a persisted frontier")
	}
	out := captureStdout(t, func() {
		if err := tailAndEmit(ctx, c, "w", "me", channel.CursorAt(off), true); err != nil {
			t.Errorf("tailAndEmit: %v", err)
		}
	})
	if !strings.Contains(out, "missed-me") {
		t.Errorf("gap message not delivered: %q", out)
	}
}

// FYIs and other-addressed messages are not wake events: one-shot must keep
// blocking through them and only exit on a record addressed to us.
func TestTailAndEmitOneShotSkipsNonWakeRecords(t *testing.T) {
	c, err := channel.Open(t.TempDir(), "w")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed, _ := c.End()

	done := make(chan string, 1)
	go func() {
		done <- captureStdout(t, func() {
			_ = tailAndEmit(ctx, c, "w", "me", seed, true)
		})
	}()

	time.Sleep(50 * time.Millisecond)
	_ = c.Append(ctx, channel.Record{Sender: "peer", Kind: channel.KindFYI, Body: "quiet fyi"})
	_ = c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Mentions: []string{"other"}, Body: "not for me"})

	select {
	case out := <-done:
		t.Fatalf("woke on a non-wake record: %q", out)
	case <-time.After(300 * time.Millisecond):
	}

	_ = c.Append(ctx, channel.Record{Sender: "peer", Kind: "msg", Mentions: []string{"me"}, Body: "for me"})
	select {
	case out := <-done:
		if strings.Contains(out, "quiet fyi") || strings.Contains(out, "not for me") {
			t.Errorf("non-wake records leaked into output: %q", out)
		}
		if !strings.Contains(out, "for me") {
			t.Errorf("directed message missing: %q", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not wake on directed message")
	}
}

func TestReadOffsetMissing(t *testing.T) {
	c, err := channel.Open(t.TempDir(), "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ReadOffset("nobody"); ok {
		t.Error("ReadOffset reported a frontier for a peer that never saved one")
	}
}
