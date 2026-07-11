package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestFeedbackRoundLifecycle drives a full round end to end: open, two members
// submit (with a duplicate across them), tally dedupes, close records the
// outcome, and the round then reads as closed.
func TestFeedbackRoundLifecycle(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()

	if err := c.OpenFeedbackRound(ctx, "amber", "r1", "round open"); err != nil {
		t.Fatalf("open: %v", err)
	}
	cur, err := c.CurrentRound()
	if err != nil || cur == nil || !cur.Open || cur.ID != "r1" || cur.Opener != "amber" {
		t.Fatalf("CurrentRound after open = %+v, err=%v", cur, err)
	}

	if _, err := c.SubmitFeedback(ctx, "compiler", "mentions are confusing"); err != nil {
		t.Fatalf("submit compiler: %v", err)
	}
	if _, err := c.SubmitFeedback(ctx, "amber", "join output too long\nMentions are  Confusing"); err != nil {
		t.Fatalf("submit amber: %v", err)
	}

	items, err := c.TallyFeedback()
	if err != nil {
		t.Fatalf("tally: %v", err)
	}
	// "mentions are confusing" (compiler) and "Mentions are  Confusing" (amber)
	// collapse to one; "join output too long" is distinct → 2 candidates.
	if len(items) != 2 {
		t.Fatalf("tally = %v, want 2 deduped items", items)
	}
	if items[0] != "mentions are confusing" || items[1] != "join output too long" {
		t.Fatalf("tally order/content = %v", items)
	}

	if _, err := c.CloseFeedbackRound(ctx, "amber", "filed"); err != nil {
		t.Fatalf("close: %v", err)
	}
	cur, err = c.CurrentRound()
	if err != nil || cur == nil || cur.Open || cur.Outcome != "filed" {
		t.Fatalf("CurrentRound after close = %+v, err=%v", cur, err)
	}
}

// TestOpenFeedbackRoundRefusesSecond verifies rounds never overlap.
func TestOpenFeedbackRoundRefusesSecond(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()
	if err := c.OpenFeedbackRound(ctx, "amber", "r1", "round open"); err != nil {
		t.Fatalf("first open: %v", err)
	}
	err := c.OpenFeedbackRound(ctx, "compiler", "r2", "round open")
	if !errors.Is(err, ErrRoundOpen) {
		t.Fatalf("second open err = %v, want ErrRoundOpen", err)
	}
	// After closing, a new round may open.
	if _, err := c.CloseFeedbackRound(ctx, "amber", "declined"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.OpenFeedbackRound(ctx, "compiler", "r2", "round open"); err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
}

// TestSubmitAndCloseRequireOpenRound verifies operations refuse without a live
// round — both on a fresh channel and after a round has closed.
func TestSubmitAndCloseRequireOpenRound(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()

	if _, err := c.SubmitFeedback(ctx, "amber", "x"); !errors.Is(err, ErrNoOpenRound) {
		t.Fatalf("submit on fresh channel err = %v, want ErrNoOpenRound", err)
	}
	if _, err := c.CloseFeedbackRound(ctx, "amber", "filed"); !errors.Is(err, ErrNoOpenRound) {
		t.Fatalf("close on fresh channel err = %v, want ErrNoOpenRound", err)
	}
	if _, err := c.TallyFeedback(); !errors.Is(err, ErrNoOpenRound) {
		t.Fatalf("tally on fresh channel err = %v, want ErrNoOpenRound", err)
	}

	if err := c.OpenFeedbackRound(ctx, "amber", "r1", "round open"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := c.CloseFeedbackRound(ctx, "amber", "declined"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := c.SubmitFeedback(ctx, "amber", "late"); !errors.Is(err, ErrNoOpenRound) {
		t.Fatalf("submit after close err = %v, want ErrNoOpenRound", err)
	}
}

// TestCurrentRoundNilWhenNever confirms a channel with no round reports nil.
func TestCurrentRoundNilWhenNever(t *testing.T) {
	c := testChannel(t)
	// Not even created yet.
	if cur, err := c.CurrentRound(); err != nil || cur != nil {
		t.Fatalf("CurrentRound on absent channel = %+v, err=%v", cur, err)
	}
	// Created but no round.
	if err := c.Append(context.Background(), Record{Sender: "amber", Kind: "join", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if cur, err := c.CurrentRound(); err != nil || cur != nil {
		t.Fatalf("CurrentRound with no poll records = %+v, err=%v", cur, err)
	}
}

// TestTallyIgnoresOtherRounds ensures a tally only counts submits for the live
// round, not submits carried over from a previously-closed round.
func TestTallyIgnoresOtherRounds(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()

	// Round 1: one submit, then closed.
	if err := c.OpenFeedbackRound(ctx, "amber", "r1", "open"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubmitFeedback(ctx, "amber", "stale item from r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CloseFeedbackRound(ctx, "amber", "filed"); err != nil {
		t.Fatal(err)
	}
	// Round 2: distinct submit.
	if err := c.OpenFeedbackRound(ctx, "amber", "r2", "open"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubmitFeedback(ctx, "amber", "fresh item for r2"); err != nil {
		t.Fatal(err)
	}
	items, err := c.TallyFeedback()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0] != "fresh item for r2" {
		t.Fatalf("tally = %v, want only the r2 item", items)
	}
}

// TestFeedbackRecordJSONGolden pins the on-disk bytes of a round-tagged record:
// the new "round" field is appended last and omitted when empty.
func TestFeedbackRecordJSONGolden(t *testing.T) {
	r := Record{
		Ts:     "2026-01-01T00:00:00Z",
		Sender: "amber",
		Kind:   KindPollSubmit,
		Body:   "join output too long",
		Round:  "r1",
	}
	want := `{"ts":"2026-01-01T00:00:00Z","sender":"amber","kind":"poll-submit","body":"join output too long","round":"r1"}` + "\n"
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		t.Fatal(err)
	}
	if buf.String() != want {
		t.Errorf("schema drift:\n got: %s\nwant: %s", buf.String(), want)
	}
}

// TestPollRecordsRoundTrip confirms the new kinds survive a write+Read cycle
// with their round id intact.
func TestPollRecordsRoundTrip(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()
	if err := c.OpenFeedbackRound(ctx, "amber", "r7", "open"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SubmitFeedback(ctx, "amber", "a\nb"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CloseFeedbackRound(ctx, "amber", "empty"); err != nil {
		t.Fatal(err)
	}
	records, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range records {
		if strings.HasPrefix(r.Kind, "poll-") {
			if r.Round != "r7" {
				t.Fatalf("record %q has round %q, want r7", r.Kind, r.Round)
			}
			kinds = append(kinds, r.Kind)
		}
	}
	got := strings.Join(kinds, ",")
	want := KindPollOpen + "," + KindPollSubmit + "," + KindPollClose
	if got != want {
		t.Fatalf("poll kinds = %q, want %q", got, want)
	}
}
