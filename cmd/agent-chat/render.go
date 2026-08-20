package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/akostibas/agent-chat-skill/channel"
)

// Rendered-size budgets for injected context. Long lines and huge records cost
// the receiving agent context for no gain, so a body is wrapped per line and
// the whole record capped; past the cap emission stops at a line boundary with
// an exact recovery command appended (issue #37). Widths count HTML entity
// escaping (< > & become &lt; &gt; &amp;), which the injection path applies.
const (
	lineBudget    = 400  // max rendered chars per emitted line, prefix included
	eventBudget   = 2000 // max rendered chars per record before self-truncating
	footerReserve = 250  // rendered chars held back for the recovery footer
)

// renderedWidth returns the size of s as the notification path renders it,
// counting entity-escaped runes at their escaped width.
func renderedWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch r {
	case '<', '>':
		return 4 // &lt; / &gt;
	case '&':
		return 5 // &amp;
	}
	return 1
}

// wrapLine splits line into pieces whose rendered width each fits budget,
// breaking at the last space where possible and mid-word otherwise.
func wrapLine(line string, budget int) []string {
	if budget < 5 { // degenerate prefix wider than the budget; emit as-is
		return []string{line}
	}
	var out []string
	runes := []rune(line)
	for len(runes) > 0 {
		w, cut, lastSpace := 0, len(runes), -1
		for i, r := range runes {
			rw := runeWidth(r)
			if w+rw > budget {
				cut = i
				break
			}
			w += rw
			if r == ' ' {
				lastSpace = i
			}
		}
		if cut == len(runes) {
			out = append(out, string(runes))
			break
		}
		if lastSpace > 0 {
			out = append(out, string(runes[:lastSpace]))
			runes = runes[lastSpace+1:]
		} else {
			out = append(out, string(runes[:cut]))
			runes = runes[cut:]
		}
	}
	return out
}

// emitStreamRecord writes one record to w in the channel's display format:
//
//	sender │ [ts kind] cwd=... branch=...
//	sender │ <body line 1>
//	sender │ <body line 2>
//
// Body lines are wrapped to lineBudget rendered chars, and the whole record is
// capped at eventBudget: past it, emission stops at a line boundary and a
// footer names the exact history command that recovers the full text.
func emitStreamRecord(w io.Writer, r channel.Record, slug string) {
	var header strings.Builder
	header.WriteString(r.Sender)
	header.WriteString(" │ [")
	header.WriteString(r.Ts)
	header.WriteString(" ")
	header.WriteString(r.Kind)
	header.WriteString("]")
	if r.Cwd != "" {
		header.WriteString(" cwd=")
		header.WriteString(r.Cwd)
	}
	if r.Branch != "" {
		header.WriteString(" branch=")
		header.WriteString(r.Branch)
	}
	_, _ = fmt.Fprintln(w, header.String())

	prefix := r.Sender + " │ "
	prefixW := renderedWidth(prefix)

	var lines []string
	for _, bodyLine := range strings.Split(r.Body, "\n") {
		lines = append(lines, wrapLine(bodyLine, lineBudget-prefixW)...)
	}

	total := renderedWidth(header.String())
	fits := total
	for _, l := range lines {
		fits += prefixW + renderedWidth(l)
	}

	for i, l := range lines {
		lw := prefixW + renderedWidth(l)
		if fits > eventBudget && total+lw > eventBudget-footerReserve {
			_, _ = fmt.Fprintf(w, "%s…(agent-chat: truncated — %d more lines; full text: history.sh %s --since %s)\n",
				prefix, len(lines)-i, slug, r.Ts)
			return
		}
		_, _ = fmt.Fprintln(w, prefix+l)
		total += lw
	}
}
