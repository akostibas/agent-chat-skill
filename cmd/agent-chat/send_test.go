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
		wantOK   bool
	}{
		{"bare message is refused", "just a note, no mention", nil, false},
		{"prose with no mention is refused", "deploying now", nil, false},
		{"@all broadcasts", "@all heads up everyone", nil, true},
		{"@all case-insensitive", "@All ship it", nil, true},
		{"@all wins over names", "@all cc @bob", nil, true},
		{"present member addresses", "@coordinator starting SWA-1", []string{"coordinator"}, true},
		{"multiple present members union", "@alice @bob look here", []string{"alice", "bob"}, true},
		{"member + prose token → member only", "ping @bob and check @vercel/otel", []string{"bob"}, true},
		{"unknown/prose token only → refused", "heads up: @vercel/otel changed", nil, false},
		{"absent peer only → refused (no broadcast fallback)", "@nobody-here ping", nil, false},
		{"typo'd name → refused, not sprayed", "@coordintor starting", nil, false},
		{"email is not a mention → refused", "reach me at me@example.com", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveAudience(tc.body, members)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !reflect.DeepEqual(got, tc.wantMent) {
				t.Fatalf("mentions = %v, want %v", got, tc.wantMent)
			}
		})
	}
}
