package channel

import "sort"

// ExtractMentions finds @token mentions in body using the channel's boundary
// rules: an @token is a mention when it is not preceded or followed by an
// identifier byte ([a-zA-Z0-9_-]), so "user@example.com" and "@vercel/otel"
// (the slash stops the scan after "vercel") behave as the shell scripts did.
// Tokens longer than 40 bytes are ignored. The result is deduplicated and
// sorted, matching the original jq `unique`.
func ExtractMentions(body string) []string {
	var mentions []string
	seen := map[string]bool{}
	b := []byte(body)
	for i := 0; i < len(b); i++ {
		if b[i] != '@' {
			continue
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
