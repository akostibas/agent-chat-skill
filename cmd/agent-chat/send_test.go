package main

import (
	"reflect"
	"testing"
)

func TestResolveAudience(t *testing.T) {
	members := []string{"alice", "bob", "coordinator"}
	cases := []struct {
		name      string
		body      string
		wantMent  []string
		wantUnmat []string
		wantAud   audience
	}{
		// No @-token at all → a deliberate FYI (logged, wakes no one).
		{"bare message → FYI", "just a note, no mention", nil, nil, audienceFYI},
		{"prose with no mention → FYI", "deploying now", nil, nil, audienceFYI},
		{"email is not a mention → FYI", "reach me at me@example.com", nil, nil, audienceFYI},
		// @all → broadcast.
		{"@all broadcasts", "@all heads up everyone", nil, nil, audienceBroadcast},
		{"@all case-insensitive", "@All ship it", nil, nil, audienceBroadcast},
		{"@all wins over names", "@all cc @bob", nil, nil, audienceBroadcast},
		// Present @<name> → directed.
		{"present member addresses", "@coordinator starting SWA-1", []string{"coordinator"}, nil, audienceDirected},
		{"multiple present members union", "@alice @bob look here", []string{"alice", "bob"}, nil, audienceDirected},
		{"member + prose token → member only", "ping @bob and check @vercel/otel", []string{"bob"}, nil, audienceDirected},
		// @-token present but no present member matches → refused (likely a typo,
		// not a quiet note; an @-token signals intent to address someone).
		{"unknown/prose token only → refused", "heads up: @vercel/otel changed", nil, []string{"vercel"}, audienceRefuse},
		{"absent peer only → refused", "@nobody-here ping", nil, []string{"nobody-here"}, audienceRefuse},
		{"typo'd name → refused", "@coordintor starting", nil, []string{"coordintor"}, audienceRefuse},

		// ── issue #57: a quoted @-token is content, not an address ───────────────
		// Both messages that were refused live on the channel must now send
		// first-try, as FYIs — quoting addresses no one and wakes no one.
		{"quoted bot command sends as FYI", "to rebase it you comment `@dependabot rebase` on the PR", nil, nil, audienceFYI},
		{"quoted scoped package sends as FYI", "ADR-0010's own example is `@vercel/otel`", nil, nil, audienceFYI},
		{"fenced block quotes tokens", "```\n@dependabot rebase\n@vercel/otel\n```", nil, nil, audienceFYI},
		{"quoted token does not count toward addressed set", "@bob see `@vercel/otel`", []string{"bob"}, nil, audienceDirected},
		{"quoting does not suppress a real address", "`@vercel/otel` — @alice take this", []string{"alice"}, nil, audienceDirected},
		{"quoted @all does not broadcast", "type `@all` to broadcast", nil, nil, audienceFYI},

		// ADR-0010 must not regress: quoting is opt-in per token, so an unquoted
		// typo still fails loudly even alongside a quoted one.
		{"typo'd name still refused next to a quoted token", "`@dependabot rebase` cc @coordintor", nil, []string{"coordintor"}, audienceRefuse},
		{"unterminated backtick does not quote", "a stray ` then @coordintor", nil, []string{"coordintor"}, audienceRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unmatched, aud := resolveAudience(tc.body, members)
			if aud != tc.wantAud {
				t.Fatalf("audience = %v, want %v", aud, tc.wantAud)
			}
			if !reflect.DeepEqual(got, tc.wantMent) {
				t.Fatalf("mentions = %v, want %v", got, tc.wantMent)
			}
			if !reflect.DeepEqual(unmatched, tc.wantUnmat) {
				t.Fatalf("unmatched = %v, want %v", unmatched, tc.wantUnmat)
			}
		})
	}
}

// The refusal has to teach the escape, or nobody finds it (issue #57 AC).
func TestQuoteTokensNamesTheOffender(t *testing.T) {
	if got := quoteTokens([]string{"vercel"}); got != "@vercel" {
		t.Errorf("quoteTokens single = %q, want @vercel", got)
	}
	if got := quoteTokens([]string{"vercel", "coordintor"}); got != "@vercel, @coordintor" {
		t.Errorf("quoteTokens multi = %q", got)
	}
	if got := firstToken([]string{"dependabot"}); got != "@dependabot" {
		t.Errorf("firstToken = %q, want @dependabot", got)
	}
	// Defensive paths still produce something printable rather than panicking.
	if got := quoteTokens(nil); got == "" {
		t.Error("quoteTokens(nil) must not be empty")
	}
	if got := firstToken(nil); got == "" {
		t.Error("firstToken(nil) must not be empty")
	}
}

// The refusal leads with the fix that actually applies: "did you mean" for a
// near-miss name, the quoting escape for content. Ordering only — a near miss
// still refuses (ADR-0013 keeps guessing out of the decision).
func TestNearestMemberOrdersTheAdvice(t *testing.T) {
	members := []string{"zippy-ferret", "dapper-urial"}
	cases := []struct {
		name   string
		tokens []string
		want   string
	}{
		{"one extra char is a typo", []string{"zippy-ferrett"}, "zippy-ferret"},
		{"transposed chars are a typo", []string{"zippy-ferrte"}, "zippy-ferret"},
		{"case-insensitive", []string{"Zippy-Ferret"}, "zippy-ferret"},
		{"package name is not a typo", []string{"vercel"}, ""},
		{"bot command is not a typo", []string{"dependabot"}, ""},
		{"far-off name is not a typo", []string{"nobody-here"}, ""},
		{"no tokens", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nearestMember(tc.tokens, members); got != tc.want {
				t.Errorf("nearestMember(%v) = %q, want %q", tc.tokens, got, tc.want)
			}
		})
	}
}
