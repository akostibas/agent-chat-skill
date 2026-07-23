package main

import (
	"reflect"
	"testing"
)

func TestResolveAudience(t *testing.T) {
	members := []string{"alice", "bob", "coordinator"}
	cases := []struct {
		name     string
		body     string
		wantMent []string
		wantAud  audience
	}{
		// No @-token at all → a deliberate FYI (logged, wakes no one).
		{"bare message → FYI", "just a note, no mention", nil, audienceFYI},
		{"prose with no mention → FYI", "deploying now", nil, audienceFYI},
		{"email is not a mention → FYI", "reach me at me@example.com", nil, audienceFYI},
		// @all → broadcast.
		{"@all broadcasts", "@all heads up everyone", nil, audienceBroadcast},
		{"@all case-insensitive", "@All ship it", nil, audienceBroadcast},
		{"@all wins over names", "@all cc @bob", nil, audienceBroadcast},
		// Present @<name> → directed.
		{"present member addresses", "@coordinator starting SWA-1", []string{"coordinator"}, audienceDirected},
		{"multiple present members union", "@alice @bob look here", []string{"alice", "bob"}, audienceDirected},
		{"member + prose token → member only", "ping @bob and check @vercel/otel", []string{"bob"}, audienceDirected},
		// @-token present but no present member matches → refused (likely a typo,
		// not a quiet note; an @-token signals intent to address someone).
		{"unknown/prose token only → refused", "heads up: @vercel/otel changed", nil, audienceRefuse},
		{"absent peer only → refused", "@nobody-here ping", nil, audienceRefuse},
		{"typo'd name → refused", "@coordintor starting", nil, audienceRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, aud := resolveAudience(tc.body, members)
			if aud != tc.wantAud {
				t.Fatalf("audience = %v, want %v", aud, tc.wantAud)
			}
			if !reflect.DeepEqual(got, tc.wantMent) {
				t.Fatalf("mentions = %v, want %v", got, tc.wantMent)
			}
		})
	}
}
