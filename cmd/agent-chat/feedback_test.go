package main

import "testing"

// TestFeedbackRoll pins the boundary behavior and checks the default rate lands
// near 10% over many draws (wide tolerance so real CSPRNG entropy never flakes).
func TestFeedbackRoll(t *testing.T) {
	// rate <= 0 never hits; rate >= 1 always hits.
	for i := 0; i < 1000; i++ {
		if feedbackRoll(0) {
			t.Fatal("feedbackRoll(0) returned true; must be disabled")
		}
		if !feedbackRoll(1) {
			t.Fatal("feedbackRoll(1) returned false; must always hit")
		}
	}

	const trials = 100_000
	hits := 0
	for i := 0; i < trials; i++ {
		if feedbackRoll(defaultFeedbackRate) {
			hits++
		}
	}
	// Expected ~10_000, 3σ ≈ ±285. This window is ~7σ wide → effectively never flakes.
	if hits < 9_000 || hits > 11_000 {
		t.Fatalf("feedbackRoll(%v): %d/%d hits, want ~10%%", defaultFeedbackRate, hits, trials)
	}
}

func TestEnvFloat(t *testing.T) {
	const key = "AGENT_CHAT_TEST_FLOAT"

	// Unset → default. Each t.Setenv below is auto-restored at subtest end, so
	// this reads a genuinely-unset var.
	if got := envFloat(key, 0.10); got != 0.10 {
		t.Errorf("unset: envFloat = %v, want 0.10", got)
	}

	cases := []struct {
		val  string
		want float64
	}{
		{"0.25", 0.25}, // parsed
		{"0", 0},       // explicit zero is honored (disable)
		{"abc", 0.10},  // unparseable → default
		{"-1", 0.10},   // negative → default
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv(key, tc.val)
			if got := envFloat(key, 0.10); got != tc.want {
				t.Errorf("envFloat(%q, def=0.10) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}
