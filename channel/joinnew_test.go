package channel

import (
	"context"
	"testing"
)

// TestJoinNewOpensRoundOnCreateOnly verifies the once-per-channel semantics:
// the channel-creating join opens a round when asked, a later join into the
// same channel is Created=false and never opens a second round even when it also
// carries a PollOpen request.
func TestJoinNewOpensRoundOnCreateOnly(t *testing.T) {
	c := testChannel(t)
	ctx := context.Background()
	open := &PollOpen{RoundID: "r1", Body: "open"}

	res, err := c.JoinNew(ctx, Record{Sender: "amber", Kind: "join", Body: "hi"}, open)
	if err != nil {
		t.Fatalf("first join: %v", err)
	}
	if !res.Created || !res.Opened {
		t.Fatalf("creator join = %+v, want Created && Opened", res)
	}
	round, _ := c.CurrentRound()
	if round == nil || !round.Open || round.ID != "r1" {
		t.Fatalf("round after create = %+v, want open r1", round)
	}

	// A second joiner that ALSO carries a PollOpen must not open a second round.
	res2, err := c.JoinNew(ctx, Record{Sender: "compiler", Kind: "join", Body: "hi"},
		&PollOpen{RoundID: "r2", Body: "open"})
	if err != nil {
		t.Fatalf("second join: %v", err)
	}
	if res2.Created || res2.Opened {
		t.Fatalf("second join = %+v, want neither Created nor Opened", res2)
	}
	round, _ = c.CurrentRound()
	if round.ID != "r1" {
		t.Fatalf("round after second join = %+v, want still r1 (no second round)", round)
	}
}

// TestJoinNewNoRoundWhenNil confirms a creating join without a PollOpen request
// (the roll missed) creates the channel but opens no round.
func TestJoinNewNoRoundWhenNil(t *testing.T) {
	c := testChannel(t)
	res, err := c.JoinNew(context.Background(), Record{Sender: "amber", Kind: "join", Body: "hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Opened {
		t.Fatalf("join = %+v, want Created && !Opened", res)
	}
	if round, _ := c.CurrentRound(); round != nil {
		t.Fatalf("round = %+v, want none", round)
	}
}

// TestJoinDelegatesToJoinNew guards the back-compat wrapper: Join still returns
// the claimed name and opens nothing.
func TestJoinDelegatesToJoinNew(t *testing.T) {
	c := testChannel(t)
	name, err := c.Join(context.Background(), Record{Sender: "amber", Kind: "join", Body: "hi"})
	if err != nil || name != "amber" {
		t.Fatalf("Join = %q, %v", name, err)
	}
	if round, _ := c.CurrentRound(); round != nil {
		t.Fatalf("Join opened a round unexpectedly: %+v", round)
	}
}
