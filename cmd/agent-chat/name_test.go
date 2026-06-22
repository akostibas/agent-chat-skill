package main

import (
	"strings"
	"testing"
)

// Generated names must always pass the identity regex join/send/stream validate
// against — otherwise an auto-generated name would be rejected by the very
// commands that consume it.
func TestGenerateNameValid(t *testing.T) {
	seen := make(map[string]int)
	for range 500 {
		n := generateName()
		if !identRE.MatchString(n) {
			t.Fatalf("generated name %q does not match identity regex", n)
		}
		if !strings.Contains(n, "-") {
			t.Fatalf("generated name %q is not adjective-animal shaped", n)
		}
		seen[n]++
	}
	// Sanity check on entropy: 500 draws from ~2300 combos should yield many
	// distinct names, not a single clustered pick.
	if len(seen) < 100 {
		t.Errorf("generateName looks low-entropy: only %d distinct names in 500 draws", len(seen))
	}
}

func TestWordlistsDisjoint(t *testing.T) {
	adj := make(map[string]bool, len(nameAdjectives))
	for _, a := range nameAdjectives {
		adj[a] = true
	}
	for _, a := range nameAnimals {
		if adj[a] {
			t.Errorf("word %q appears in both wordlists; names must read adjective-animal", a)
		}
	}
}
