package channel

import (
	"context"
	"errors"
	"strings"
)

// Feedback-poll record kinds. A poll round lets a channel harvest friction /
// process-improvement items from its live members, agree on a candidate list,
// and hand it to a coordinator to file as issues. The round's entire lifecycle
// lives in the append-only log (event-sourced) — there is no separate state
// file, so replaying the log reconstructs the round. See issues #31/#32.
const (
	KindPollOpen   = "poll-open"
	KindPollSubmit = "poll-submit"
	KindPollClose  = "poll-close"
)

var (
	// ErrRoundOpen is returned by OpenFeedbackRound when a round is already live
	// on the channel — rounds never overlap.
	ErrRoundOpen = errors.New("channel: a feedback round is already open")
	// ErrNoOpenRound is returned by SubmitFeedback/CloseFeedbackRound/TallyFeedback
	// when no round is currently open.
	ErrNoOpenRound = errors.New("channel: no open feedback round")
)

// FeedbackRound is the reconstructed state of one poll round.
type FeedbackRound struct {
	ID      string // opaque round id assigned at open
	Opener  string // member that opened the round
	Open    bool   // true until a matching poll-close is recorded
	Outcome string // terminal outcome, from the poll-close body, once closed
}

// CurrentRound replays the log and returns the most recent round's state, or nil
// if no round was ever opened on the channel. It is a pure read. Rounds never
// overlap (OpenFeedbackRound refuses while one is live), so a single forward
// pass over the log is sufficient to reconstruct the latest round.
func (c *Channel) CurrentRound() (*FeedbackRound, error) {
	if !c.Exists() {
		return nil, nil
	}
	records, err := c.Read()
	if err != nil {
		return nil, err
	}
	var cur *FeedbackRound
	for _, r := range records {
		switch r.Kind {
		case KindPollOpen:
			cur = &FeedbackRound{ID: r.Round, Opener: r.Sender, Open: true}
		case KindPollClose:
			if cur != nil && cur.ID == r.Round {
				cur.Open = false
				cur.Outcome = r.Body
			}
		}
	}
	return cur, nil
}

// openRound returns the live round, or nil if none is open.
func (c *Channel) openRound() (*FeedbackRound, error) {
	cur, err := c.CurrentRound()
	if err != nil {
		return nil, err
	}
	if cur == nil || !cur.Open {
		return nil, nil
	}
	return cur, nil
}

// OpenFeedbackRound opens a poll round identified by roundID, refusing with
// ErrRoundOpen if one is already live. The open-round check and the poll-open
// append happen under the same channel lock, so two racing openers cannot both
// create a round — the mechanism the channel-creating join relies on to open a
// round atomically with the first join record (issue #33). body is the
// human/agent-facing announcement (e.g. how to submit).
func (c *Channel) OpenFeedbackRound(ctx context.Context, opener, roundID, body string) error {
	if err := c.ensureDir(); err != nil {
		return err
	}
	lockF, err := c.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer releaseLock(lockF)

	cur, err := c.CurrentRound()
	if err != nil {
		return err
	}
	if cur != nil && cur.Open {
		return ErrRoundOpen
	}
	return c.appendLocked(Record{
		Ts:     isoNow(),
		Sender: opener,
		Kind:   KindPollOpen,
		Round:  roundID,
		Body:   body,
	})
}

// SubmitFeedback attaches items (one per line in body) from sender to the open
// round and returns the round id it attached to. Returns ErrNoOpenRound if no
// round is live. The check and append are serialized under the channel lock.
func (c *Channel) SubmitFeedback(ctx context.Context, sender, body string) (string, error) {
	if err := c.ensureDir(); err != nil {
		return "", err
	}
	lockF, err := c.acquireLock(ctx)
	if err != nil {
		return "", err
	}
	defer releaseLock(lockF)

	cur, err := c.CurrentRound()
	if err != nil {
		return "", err
	}
	if cur == nil || !cur.Open {
		return "", ErrNoOpenRound
	}
	return cur.ID, c.appendLocked(Record{
		Ts:     isoNow(),
		Sender: sender,
		Kind:   KindPollSubmit,
		Round:  cur.ID,
		Body:   body,
	})
}

// CloseFeedbackRound records a terminal outcome for the open round and returns
// its id. Returns ErrNoOpenRound if no round is live.
func (c *Channel) CloseFeedbackRound(ctx context.Context, sender, outcome string) (string, error) {
	if err := c.ensureDir(); err != nil {
		return "", err
	}
	lockF, err := c.acquireLock(ctx)
	if err != nil {
		return "", err
	}
	defer releaseLock(lockF)

	cur, err := c.CurrentRound()
	if err != nil {
		return "", err
	}
	if cur == nil || !cur.Open {
		return "", ErrNoOpenRound
	}
	return cur.ID, c.appendLocked(Record{
		Ts:     isoNow(),
		Sender: sender,
		Kind:   KindPollClose,
		Round:  cur.ID,
		Body:   outcome,
	})
}

// TallyFeedback returns the deduplicated union of items submitted to the open
// round, in first-seen order. It is a pure read (no mutation). Each poll-submit
// body is split one-item-per-line; obvious duplicates (case- and
// whitespace-insensitive) collapse to their first occurrence. Fuzzy/near-
// duplicate consolidation is the coordinator's judgment call (#34), not this
// primitive's. Returns ErrNoOpenRound if no round is live.
func (c *Channel) TallyFeedback() ([]string, error) {
	cur, err := c.openRound()
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, ErrNoOpenRound
	}
	records, err := c.Read()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var items []string
	for _, r := range records {
		if r.Kind != KindPollSubmit || r.Round != cur.ID {
			continue
		}
		for _, line := range strings.Split(r.Body, "\n") {
			item := strings.TrimSpace(line)
			if item == "" {
				continue
			}
			key := normalizeItem(item)
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, item)
		}
	}
	return items, nil
}

// normalizeItem collapses case and interior whitespace so trivially-different
// phrasings of the same item ("Join output too long" vs "join output  too long")
// dedupe to one key.
func normalizeItem(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
