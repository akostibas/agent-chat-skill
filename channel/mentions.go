package channel

import "sort"

// ExtractMentions finds @token mentions in body using the channel's boundary
// rules: an @token is a mention when it is not preceded or followed by an
// identifier byte ([a-zA-Z0-9_-]), so "user@example.com" and "@vercel/otel"
// (the slash stops the scan after "vercel") behave as the shell scripts did.
// Tokens longer than 40 bytes are ignored. The result is deduplicated and
// sorted, matching the original jq `unique`.
//
// An @token inside a markdown code span is quoted content, not an address, and
// is not returned — so a body may name a bot command or a scoped package
// verbatim without addressing anyone. See CodeSpanMask for the span rules.
func ExtractMentions(body string) []string {
	var mentions []string
	seen := map[string]bool{}
	b := []byte(body)
	quoted := CodeSpanMask(body)
	for i := 0; i < len(b); i++ {
		if b[i] != '@' {
			continue
		}
		if quoted[i] {
			continue // inside a code span: quoted content, not an address
		}
		if i > 0 && isIdentByte(b[i-1]) {
			continue
		}
		j := i + 1
		for j < len(b) && isIdentByte(b[j]) {
			j++
		}
		token := string(b[i+1 : j])
		if len(token) == 0 || len(token) > 40 {
			continue
		}
		if j < len(b) && isIdentByte(b[j]) {
			continue
		}
		if !seen[token] {
			seen[token] = true
			mentions = append(mentions, token)
		}
	}
	sort.Strings(mentions)
	return mentions
}

// CodeSpanMask marks the bytes of body that sit inside a markdown code span,
// delimiters included. A run of N backticks opens a span that the next run of
// exactly N backticks closes; an opening run with no matching close is literal
// text and masks nothing, so a lone stray backtick can't silence the rest of a
// message. Fenced blocks fall out of the same rule (``` opens, ``` closes).
//
// The mask is what makes an @token quotable: `@dependabot` is content, while a
// bare @dependabot is still an address. Exported because peers that import this
// package resolve mentions themselves and need the same notion of "quoted".
func CodeSpanMask(body string) []bool {
	b := []byte(body)
	mask := make([]bool, len(b))
	for i := 0; i < len(b); {
		if b[i] != '`' {
			i++
			continue
		}
		open := i
		for i < len(b) && b[i] == '`' {
			i++
		}
		runLen := i - open
		// Look for a closing run of exactly runLen backticks.
		for j := i; j < len(b); {
			if b[j] != '`' {
				j++
				continue
			}
			close := j
			for j < len(b) && b[j] == '`' {
				j++
			}
			if j-close != runLen {
				continue // wrong length: keep looking
			}
			for k := open; k < j; k++ {
				mask[k] = true
			}
			i = j
			break
		}
		// No closing run: the opening backticks are literal. i already sits past
		// them, so scanning resumes after the run and a later pair can still span.
	}
	return mask
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_' || c == '-'
}

// FilterMentions retains only mention tokens that name a present channel
// member; mentions of unknown names are dropped so the message broadcasts
// rather than addressing nobody.
func FilterMentions(mentions, members []string) []string {
	mset := make(map[string]bool, len(members))
	for _, m := range members {
		mset[m] = true
	}
	var out []string
	for _, m := range mentions {
		if mset[m] {
			out = append(out, m)
		}
	}
	return out
}
