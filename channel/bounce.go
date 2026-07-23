package channel

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
)

// KindBounce is the record kind for an undeliverable notice: a message that
// named a peer who left the channel before reading it. The reaper posts one to
// the original sender when it retires a departed peer with directed messages
// still unread, so a coordinator learns its dispatch went nowhere instead of
// waiting on a reply that can never come. See ADR-0011.
const KindBounce = "bounce"

// bounceSnippetMax caps how much of the original message body a bounce echoes
// back, so a large undelivered message doesn't reproduce itself in full on the
// sender's side. The sender wrote it — it only needs enough to recognize which
// message bounced.
const bounceSnippetMax = 120

// SaveReadOffset records name's read frontier — the byte offset in the log up to
// which name has received every record. A long-lived consumer (the stream, or an
// external Go peer) calls it as it delivers, so that if the consumer later
// vanishes, the reaper can tell which directed messages it never read and bounce
// them to their senders (see ReapStale / ADR-0011). Best-effort and cheap: it
// writes a few bytes to cursors/<name>. A peer that never calls it is treated as
// having read nothing, so all of its directed messages bounce.
func (c *Channel) SaveReadOffset(name string, off int64) error {
	if err := os.MkdirAll(c.cursorsDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(c.cursorFile(name), []byte(strconv.FormatInt(off, 10)), 0644)
}

// ClearReadOffset removes name's persisted read frontier. Pair it with a clean
// leave (RemovePresence): a peer that departs gracefully has no undelivered
// messages to bounce, so its frontier is just litter. A missing file is not an
// error.
func (c *Channel) ClearReadOffset(name string) error {
	err := os.Remove(c.cursorFile(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// readOffset returns name's persisted read frontier, or 0 if none is recorded or
// it can't be parsed. Defaulting to 0 is deliberately conservative: a peer with
// no recorded frontier is assumed to have read nothing, so its directed messages
// bounce rather than being silently assumed delivered.
func (c *Channel) readOffset(name string) int64 {
	data, err := os.ReadFile(c.cursorFile(name))
	if err != nil {
		return 0
	}
	off, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || off < 0 {
		return 0
	}
	return off
}

// bounceUndelivered posts a bounce to the sender of every directed message that
// departed left unread. The caller must hold the channel lock (ReapStale does):
// the bounces are appended in the same locked section as departed's leave, so a
// peer sees the departure and the bounce together.
//
// "Unread" is everything past departed's persisted read frontier; "directed"
// means the message named departed in its mentions (a broadcast is fire-and-
// forget and does not bounce). A bounce is skipped when its own recipient is no
// longer a member, so a departed sender can't trigger a bounce into the void and
// bounces never cascade. departed's own messages are skipped, as are non-msg
// records (a bounce never bounces).
func (c *Channel) bounceUndelivered(ctx context.Context, departed string) {
	recs, _, err := c.ReadSince(ctx, CursorAt(c.readOffset(departed)))
	if err != nil {
		return
	}
	members := make(map[string]bool)
	for _, m := range c.Members() {
		members[m] = true
	}
	for _, r := range recs {
		if r.Kind != "msg" || r.Sender == departed || !members[r.Sender] {
			continue
		}
		if !slices.Contains(r.Mentions, departed) {
			continue
		}
		_ = c.appendLocked(Record{
			Ts:       isoNow(),
			Sender:   departed,
			Kind:     KindBounce,
			Body:     bounceBody(departed, r.Body),
			Mentions: []string{r.Sender},
		})
	}
}

// bounceBody renders the notice a sender sees when a message could not be
// delivered, echoing enough of the original to identify it.
func bounceBody(departed, original string) string {
	snippet := original
	if len(snippet) > bounceSnippetMax {
		snippet = snippet[:bounceSnippetMax] + "…"
	}
	return "undeliverable: " + departed + " left the channel before reading your message: \"" + snippet + "\""
}
