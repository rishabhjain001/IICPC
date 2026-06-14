package scoring

// runs that ended badly get a zero — no partial credit
var terminalStatuses = map[string]bool{
	"SANDBOX_CRASH":         true,
	"BUILD_FAILED":          true,
	"BUILD_TIMEOUT":         true,
	"NO_ENDPOINTS":          true,
	"PROVISIONING_TIMEOUT":  true,
	"INSUFFICIENT_CAPACITY": true,
}

// TerminalFailureStatuses exported so tests can inspect the set
var TerminalFailureStatuses = terminalStatuses

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// speedScore: 1ms → 1.0, 100ms → 0.0, linear
func ComputeSpeedScore(p99Ms float64) float64 {
	return clamp((100-p99Ms)/99, 0.0, 1.0)
}

// stabilityScore: 1M TPS → 1.0
func ComputeStabilityScore(maxTPS int64) float64 {
	return clamp(float64(maxTPS)/1_000_000.0, 0.0, 1.0)
}

// weights: speed 35%, stability 35%, accuracy 30%
func ComputeCompositeScore(speed, stability, accuracy float64) float64 {
	return 0.35*speed + 0.35*stability + 0.30*accuracy
}

func ComputeCompositeScoreForRun(runStatus string, p99Ms float64, maxTPS int64, accuracyScore float64) float64 {
	if terminalStatuses[runStatus] {
		return 0.0
	}
	return ComputeCompositeScore(
		ComputeSpeedScore(p99Ms),
		ComputeStabilityScore(maxTPS),
		accuracyScore,
	)
}
