package scoring_test

import (
	"math"
	"testing"

	"github.com/iicpc/dbhp/leaderboard-service/internal/scoring"
)

// ---------------------------------------------------------------------------
// Task 13.1 — Unit tests for individual score functions
// ---------------------------------------------------------------------------

func TestComputeSpeedScore(t *testing.T) {
	tests := []struct {
		name   string
		p99Ms  float64
		want   float64
		approx bool
	}{
		{"1ms => 1.0", 1.0, 1.0, false},
		{"100ms => 0.0", 100.0, 0.0, false},
		{"50ms => ~0.505", 50.0, (100 - 50.0) / 99, true},
		{"negative clamp to 1.0", -5.0, 1.0, false},
		{"0ms => 100/99 clamped to 1.0", 0.0, 1.0, false},
		{"101ms => clamped to 0.0", 101.0, 0.0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scoring.ComputeSpeedScore(tc.p99Ms)
			if tc.approx {
				if math.Abs(got-tc.want) > 1e-9 {
					t.Errorf("ComputeSpeedScore(%v) = %v, want ~%v", tc.p99Ms, got, tc.want)
				}
			} else {
				if got != tc.want {
					t.Errorf("ComputeSpeedScore(%v) = %v, want %v", tc.p99Ms, got, tc.want)
				}
			}
			// Output must always be in [0, 1].
			if got < 0.0 || got > 1.0 {
				t.Errorf("ComputeSpeedScore(%v) = %v out of [0,1]", tc.p99Ms, got)
			}
		})
	}
}

func TestComputeStabilityScore(t *testing.T) {
	tests := []struct {
		name   string
		maxTPS int64
		want   float64
	}{
		{"1_000_000 => 1.0", 1_000_000, 1.0},
		{"0 => 0.0", 0, 0.0},
		{"500_000 => 0.5", 500_000, 0.5},
		{"negative => 0.0 (clamped)", -100, 0.0},
		{"2_000_000 => 1.0 (clamped)", 2_000_000, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scoring.ComputeStabilityScore(tc.maxTPS)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("ComputeStabilityScore(%v) = %v, want %v", tc.maxTPS, got, tc.want)
			}
			if got < 0.0 || got > 1.0 {
				t.Errorf("ComputeStabilityScore(%v) = %v out of [0,1]", tc.maxTPS, got)
			}
		})
	}
}

func TestComputeCompositeScore(t *testing.T) {
	t.Run("weights sum to 1.0", func(t *testing.T) {
		const wSpeed, wStability, wAccuracy = 0.35, 0.35, 0.30
		sum := wSpeed + wStability + wAccuracy
		if math.Abs(sum-1.0) > 1e-12 {
			t.Errorf("weight sum = %v, want 1.0", sum)
		}
	})

	t.Run("all zeros => 0.0", func(t *testing.T) {
		got := scoring.ComputeCompositeScore(0, 0, 0)
		if got != 0.0 {
			t.Errorf("CompositeScore(0,0,0) = %v, want 0.0", got)
		}
	})

	t.Run("all ones => 1.0", func(t *testing.T) {
		got := scoring.ComputeCompositeScore(1, 1, 1)
		if math.Abs(got-1.0) > 1e-12 {
			t.Errorf("CompositeScore(1,1,1) = %v, want 1.0", got)
		}
	})

	t.Run("result in [0,1] for mid values", func(t *testing.T) {
		got := scoring.ComputeCompositeScore(0.5, 0.5, 0.5)
		if got < 0.0 || got > 1.0 {
			t.Errorf("CompositeScore(0.5,0.5,0.5) = %v out of [0,1]", got)
		}
	})

	t.Run("correct weighted calculation", func(t *testing.T) {
		want := 0.35*0.9 + 0.35*0.8 + 0.30*0.7
		got := scoring.ComputeCompositeScore(0.9, 0.8, 0.7)
		if math.Abs(got-want) > 1e-12 {
			t.Errorf("CompositeScore(0.9,0.8,0.7) = %v, want %v", got, want)
		}
	})
}

func TestTerminalFailureStatusForcesZero(t *testing.T) {
	terminalStatuses := []string{
		"SANDBOX_CRASH",
		"BUILD_FAILED",
		"BUILD_TIMEOUT",
		"NO_ENDPOINTS",
		"PROVISIONING_TIMEOUT",
		"INSUFFICIENT_CAPACITY",
	}
	for _, status := range terminalStatuses {
		t.Run(status, func(t *testing.T) {
			got := scoring.ComputeCompositeScoreForRun(status, 1.0, 1_000_000, 1.0)
			if got != 0.0 {
				t.Errorf("ComputeCompositeScoreForRun(%s,...) = %v, want 0.0", status, got)
			}
		})
	}
}

func TestNonTerminalStatusComputesScore(t *testing.T) {
	got := scoring.ComputeCompositeScoreForRun("COMPLETE", 1.0, 1_000_000, 1.0)
	want := scoring.ComputeCompositeScore(1.0, 1.0, 1.0)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("ComputeCompositeScoreForRun(COMPLETE,...) = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Task 13.2 — Property tests: Composite Score weight invariant
// Validates: Requirements 9.1
// ---------------------------------------------------------------------------

// TestCompositeScoreWeightInvariant verifies that:
//  1. weights sum to exactly 1.0
//  2. CompositeScore ∈ [0,1] for all inputs in [0,1]
//  3. CompositeScore = 0 when all inputs are 0
//  4. CompositeScore = 1 when all inputs are 1
//
// **Validates: Requirements 9.1**
func TestCompositeScoreWeightInvariant(t *testing.T) {
	const wSpeed, wStability, wAccuracy = 0.35, 0.35, 0.30

	// Invariant: weight sum = 1.0 exactly.
	if wSpeed+wStability+wAccuracy != 1.0 {
		t.Fatalf("weight sum %v ≠ 1.0", wSpeed+wStability+wAccuracy)
	}

	// Table of (speed, stability, accuracy) inputs sampled across the space.
	type row struct{ speed, stability, accuracy float64 }
	cases := []row{
		{0, 0, 0},
		{1, 1, 1},
		{0.5, 0.5, 0.5},
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
		{0.25, 0.75, 0.50},
		{0.99, 0.01, 0.50},
		{0.33, 0.33, 0.34},
		{0.0, 0.0, 1.0},
		{1.0, 0.0, 0.0},
		{0.7, 0.8, 0.9},
	}

	for _, c := range cases {
		got := scoring.ComputeCompositeScore(c.speed, c.stability, c.accuracy)

		// Property: result in [0, 1].
		if got < 0.0-1e-12 || got > 1.0+1e-12 {
			t.Errorf("CompositeScore(%v,%v,%v) = %v outside [0,1]", c.speed, c.stability, c.accuracy, got)
		}

		// Property: formula matches manual calculation.
		want := wSpeed*c.speed + wStability*c.stability + wAccuracy*c.accuracy
		if math.Abs(got-want) > 1e-12 {
			t.Errorf("CompositeScore(%v,%v,%v) = %v, want %v", c.speed, c.stability, c.accuracy, got, want)
		}
	}

	// Boundary: all-zero → 0.0.
	if got := scoring.ComputeCompositeScore(0, 0, 0); got != 0.0 {
		t.Errorf("CompositeScore(0,0,0) = %v, want 0.0", got)
	}

	// Boundary: all-one → 1.0.
	if got := scoring.ComputeCompositeScore(1, 1, 1); math.Abs(got-1.0) > 1e-12 {
		t.Errorf("CompositeScore(1,1,1) = %v, want 1.0", got)
	}
}
