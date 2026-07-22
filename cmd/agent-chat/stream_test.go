package main

import (
	"strings"
	"testing"
	"time"

	"github.com/akostibas/agent-chat-skill/channel"
)

func TestRenderedWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"plain", 5},
		{"<x>", 9}, // 4 + 1 + 4
		{"a&b", 7}, // 1 + 5 + 1
		{"", 0},
	}
	for _, c := range cases {
		if got := renderedWidth(c.in); got != c.want {
			t.Errorf("renderedWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestWrapLineShortLineUntouched(t *testing.T) {
	got := wrapLine("short line", 100)
	if len(got) != 1 || got[0] != "short line" {
		t.Fatalf("wrapLine returned %q", got)
	}
}

func TestWrapLineBreaksAtSpaces(t *testing.T) {
	line := strings.Repeat("word ", 200) // 1000 chars
	pieces := wrapLine(line, 100)
	for i, p := range pieces {
		if renderedWidth(p) > 100 {
			t.Errorf("piece %d rendered width %d > 100: %q", i, renderedWidth(p), p)
		}
	}
	// No content lost beyond the collapsed break spaces.
	if joined := strings.Join(pieces, " "); strings.TrimRight(joined, " ") != strings.TrimRight(line, " ") {
		t.Errorf("content changed by wrapping")
	}
}

func TestWrapLineMidWordWhenNoSpaces(t *testing.T) {
	line := strings.Repeat("x", 950)
	pieces := wrapLine(line, 100)
	var total int
	for i, p := range pieces {
		if renderedWidth(p) > 100 {
			t.Errorf("piece %d over budget: %d", i, renderedWidth(p))
		}
		total += len(p)
	}
	if total != 950 {
		t.Errorf("lost content: %d of 950 chars", total)
	}
}

func TestWrapLineEscapeAware(t *testing.T) {
	// 200 runes of "<>" render at 4x = 1600; budget 100 must wrap accordingly.
	line := strings.Repeat("<>", 100)
	for i, p := range wrapLine(line, 100) {
		if renderedWidth(p) > 100 {
			t.Errorf("piece %d rendered width %d > 100", i, renderedWidth(p))
		}
	}
}

func emitToString(t *testing.T, body string) string {
	t.Helper()
	var sb strings.Builder
	emitStreamRecord(&sb, channel.Record{
		Ts: "2026-07-18T00:00:00Z", Kind: "msg", Sender: "tester", Body: body,
	}, "chan")
	return sb.String()
}

func TestEmitSmallMessageIntactNoFooter(t *testing.T) {
	out := emitToString(t, "hello\nworld")
	if !strings.Contains(out, "tester │ hello\n") || !strings.Contains(out, "tester │ world\n") {
		t.Fatalf("body lines missing:\n%s", out)
	}
	if strings.Contains(out, "truncated") {
		t.Fatalf("unexpected footer on small message:\n%s", out)
	}
}

func TestEmitLongSingleLineIsWrappedNotTruncated(t *testing.T) {
	body := strings.Repeat("alpha beta gamma ", 70) // ~1190 chars, one line
	out := emitToString(t, body)
	if strings.Contains(out, "truncated") {
		t.Fatalf("1.2KB message should fit wrapped without a footer:\n%s", out)
	}
	for i, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if renderedWidth(l) > lineBudget {
			t.Errorf("line %d exceeds lineBudget (%d): %q", i, renderedWidth(l), l)
		}
	}
	// All content present (modulo wrap-point spaces).
	rejoined := strings.ReplaceAll(strings.ReplaceAll(out, "tester │ ", ""), "\n", " ")
	if !strings.Contains(rejoined, "alpha beta gamma alpha") {
		t.Errorf("content mangled:\n%s", out)
	}
}

// senders returns the Sender field of each record, for terse debounce asserts.
func senders(recs []channel.Record) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.Sender)
	}
	return out
}

func timedOutLeave(name string) channel.Record {
	return channel.Record{Sender: name, Kind: "leave", Body: channel.LeaveBodyTimedOut}
}

// A timed-out leave followed by the same peer reconnecting within the window is
// a sleep flap: neither record should ever reach a subscriber (issue #39).
func TestFlapDebouncerSuppressesReapThenReconnect(t *testing.T) {
	d := newFlapDebouncer(30 * time.Second)
	t0 := time.Unix(1000, 0)

	if got := d.offer(timedOutLeave("peer"), t0); got != nil {
		t.Fatalf("timed-out leave should be withheld, emitted %v", senders(got))
	}
	// Reconnect 7s later — inside the window.
	rejoin := channel.Record{Sender: "peer", Kind: "join", Body: "reconnected"}
	if got := d.offer(rejoin, t0.Add(7*time.Second)); got != nil {
		t.Fatalf("reconnect should cancel the held leave, emitted %v", senders(got))
	}
	// Even long past the window, nothing surfaces — the leave was cancelled.
	if got := d.expired(t0.Add(10 * time.Minute)); got != nil {
		t.Fatalf("cancelled leave must never expire-emit, got %v", senders(got))
	}
}

// A timed-out leave with no reconnect is a genuine departure: withheld during
// the window, then emitted once it elapses.
func TestFlapDebouncerEmitsGenuineDeparture(t *testing.T) {
	d := newFlapDebouncer(30 * time.Second)
	t0 := time.Unix(1000, 0)

	if got := d.offer(timedOutLeave("gone"), t0); got != nil {
		t.Fatalf("leave should be withheld initially, emitted %v", senders(got))
	}
	// Still inside the window: nothing yet.
	if got := d.expired(t0.Add(29 * time.Second)); got != nil {
		t.Fatalf("leave emitted before window elapsed: %v", senders(got))
	}
	// Window elapsed: the real departure surfaces exactly once.
	got := d.expired(t0.Add(31 * time.Second))
	if len(got) != 1 || got[0].Sender != "gone" || got[0].Kind != "leave" {
		t.Fatalf("expected one leave for 'gone', got %v", got)
	}
	if again := d.expired(t0.Add(1 * time.Hour)); again != nil {
		t.Fatalf("leave emitted twice: %v", senders(again))
	}
}

// Clean leaves, ordinary messages, and joins with no pending leave are not flap
// material and must pass straight through.
func TestFlapDebouncerPassesThroughNonFlaps(t *testing.T) {
	d := newFlapDebouncer(30 * time.Second)
	now := time.Unix(1000, 0)

	cases := []channel.Record{
		{Sender: "a", Kind: "leave", Body: "left channel"},  // clean, intentional
		{Sender: "b", Kind: "join", Body: "joined channel"}, // fresh arrival, no held leave
		{Sender: "c", Kind: "msg", Body: "hi"},              // ordinary traffic
	}
	for _, r := range cases {
		got := d.offer(r, now)
		if len(got) != 1 || got[0].Sender != r.Sender {
			t.Fatalf("record %+v should pass through, got %v", r, senders(got))
		}
	}
}

// A reconnect that arrives after the leave has already expire-emitted (peer was
// genuinely gone longer than the window, then came back) is a real reconnect and
// must surface — it is not cancelling anything.
func TestFlapDebouncerReconnectAfterExpiryEmits(t *testing.T) {
	d := newFlapDebouncer(30 * time.Second)
	t0 := time.Unix(1000, 0)

	d.offer(timedOutLeave("peer"), t0)
	if got := d.expired(t0.Add(31 * time.Second)); len(got) != 1 {
		t.Fatalf("departure should have emitted, got %v", senders(got))
	}
	// Late reconnect: the held leave is gone, so this join passes through.
	rejoin := channel.Record{Sender: "peer", Kind: "join", Body: "reconnected"}
	if got := d.offer(rejoin, t0.Add(40*time.Second)); len(got) != 1 || got[0].Kind != "join" {
		t.Fatalf("late reconnect should emit as a join, got %v", got)
	}
}

// The hold window derives from the heartbeat interval (2×), tracking the env
// override rather than a second magic constant.
func TestHoldWindowDerivesFromHeartbeat(t *testing.T) {
	t.Setenv("AGENT_CHAT_HEARTBEAT_SECS", "10")
	if got := holdWindow(); got != 20*time.Second {
		t.Fatalf("holdWindow = %v, want 20s (2×10s)", got)
	}
}

func TestEmitOversizeMessageSelfCapsWithRecoveryFooter(t *testing.T) {
	body := strings.Repeat("filler words to occupy space ", 250) // ~7.2KB
	out := emitToString(t, body)
	footer := "full text: history.sh chan --since 2026-07-18T00:00:00Z"
	if !strings.Contains(out, footer) {
		t.Fatalf("missing recovery footer:\n%s", out[len(out)-300:])
	}
	if lastLine := out[strings.LastIndex(strings.TrimRight(out, "\n"), "\n")+1:]; !strings.Contains(lastLine, "truncated") {
		t.Fatalf("footer is not the last line:\n%s", lastLine)
	}
	// Total rendered size must sit under the harness event cap.
	total := 0
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		total += renderedWidth(l)
	}
	if total > eventBudget {
		t.Errorf("rendered event %d exceeds eventBudget %d", total, eventBudget)
	}
}

func TestEmitBracketHeavyBodyStaysUnderBudgets(t *testing.T) {
	body := strings.Repeat("map[string]<-chan &T{} ", 40) // escape-heavy, one line
	out := emitToString(t, body)
	for i, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if renderedWidth(l) > lineBudget {
			t.Errorf("line %d rendered %d > lineBudget: %q", i, renderedWidth(l), l)
		}
	}
	_ = out
}
