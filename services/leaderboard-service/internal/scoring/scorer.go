// Package scoring implements the composite score computation logic for the
// Leaderboard Service (Requirements 9.1–9.6).
package scoring

// TerminalFailureStatuses lists all run statuses that force CompositeScore=0.0.
// (Requirement 9.6)
var TerminalFailureStatuses = map[string]bool{
	"SANDBOX_CRASH":          true,
	"BUILD_FAILED":           true,
	"BUILD_TIMEOUT":          true,
	"NO_ENDPOINTS":           true,
	"PROVISIONING_TIMEOUT":   true,
	"INSUFFICIENT_CAPACITY":  true,
}

// clamp restricts v to the closed interval [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ComputeSpeedScore computes SpeedScore from the p99 latency in milliseconds.
//
//	SpeedScore = clamp((100 - p99_ms) / 99, 0.0, 1.0)
//
// A p99 latency of ≤1 ms maps to 1.0; ≥100 ms maps to 0.0.
// (Requirement 9.2)
func ComputeSpeedScore(p99Ms float64) float64 {
	return clamp((100-p99Ms)/99, 0.0, 1.0)
}

// ComputeStabilityScore computes StabilityScore from the maximum sustained TPS.
//
//	StabilityScore = clamp(max_tps / 1_000_000, 0.0, 1.0)
//
// A TPS of ≥1,000,000 maps to 1.0; 0 maps to 0.0.
// (Requirement 9.3)
func ComputeStabilityScore(maxTPS int64) float64 {
	return clamp(float64(maxTPS)/1_000_000.0, 0.0, 1.0)
}

// ComputeCompositeScore computes the weighted composite score.
//
//	CompositeScore = 0.35*SpeedScore + 0.35*StabilityScore + 0.30*AccuracyScore
//
// (Requirement 9.1)
func ComputeCompositeScore(speedScore, stabilityScore, accuracyScore float64) float64 {
	return 0.35*speedScore + 0.35*stabilityScore + 0.30*accuracyScore
}

// ComputeCompositeScoreForRun returns 0.0 if runStatus is a terminal failure
// status, otherwise computes CompositeScore from the supplied metric inputs.
// accuracyScore defaults to 0.0 if unavailable (pass 0.0 explicitly).
// (Requirements 9.4, 9.6)
func ComputeCompositeScoreForRun(runStatus string, p99Ms float64, maxTPS int64, accuracyScore float64) float64 {
	if TerminalFailureStatuses[runStatus] {
		return 0.0
	}
	speed := ComputeSpeedScore(p99Ms)
	stability := ComputeStabilityScore(maxTPS)
	return ComputeCompositeScore(speed, stability, accuracyScore)
}
