package main

import (
	"strings"
	"testing"

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
