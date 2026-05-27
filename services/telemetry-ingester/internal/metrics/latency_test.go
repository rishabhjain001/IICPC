package metrics

import (
	"testing"
	"time"
)

// base time used across tests to avoid flakiness from time.Now().
var baseTime = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// toNs converts a time.Time to nanoseconds since Unix epoch.
func toNs(t time.Time) int64 { return t.UnixNano() }

// nsLatency returns a send time and recv time that produce the given latency
// in microseconds starting from base.
func nsLatency(base time.Time, latencyUs float64) (sendNs, recvNs int64) {
	sendNs = base.UnixNano()
	recvNs = sendNs + int64(latencyUs*1_000)
	return
}

// --- p50/p90/p99 with known distribution ------------------------------------

// TestPercentilesKnownDistribution adds 10 samples with latencies
// 10, 20, 30, 40, 50, 60, 70, 80, 90, 100 µs and verifies the percentiles.
func TestPercentilesKnownDistribution(t *testing.T) {
	w := NewLatencyWindow()
	latencies := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	for i, lat := range latencies {
		eventTime := baseTime.Add(time.Duration(i) * time.Second)
		send, recv := nsLatency(eventTime, lat)
		w.Add(eventTime, send, recv)
	}

	p50, p90, p99 := w.FullPopulationPercentiles()

	// With 10 samples sorted [10..100]:
	//   p50 → index ceil(0.5*10)-1 = 4 → 50
	//   p90 → index ceil(0.9*10)-1 = 8 → 90
	//   p99 → index ceil(0.99*10)-1 = 9 → 100
	if p50 != 50 {
		t.Errorf("p50: got %.1f, want 50", p50)
	}
	if p90 != 90 {
		t.Errorf("p90: got %.1f, want 90", p90)
	}
	if p99 != 100 {
		t.Errorf("p99: got %.1f, want 100", p99)
	}
}

// TestPercentilesEmptyWindow verifies that Percentiles returns (0,0,0) when
// no samples have been added.
func TestPercentilesEmptyWindow(t *testing.T) {
	w := NewLatencyWindow()
	p50, p90, p99 := w.Percentiles()
	if p50 != 0 || p90 != 0 || p99 != 0 {
		t.Errorf("expected (0,0,0) for empty window, got (%.2f, %.2f, %.2f)", p50, p90, p99)
	}
}

// TestFullPopulationPercentilesEmptyWindow verifies (0,0,0) on an empty history.
func TestFullPopulationPercentilesEmptyWindow(t *testing.T) {
	w := NewLatencyWindow()
	p50, p90, p99 := w.FullPopulationPercentiles()
	if p50 != 0 || p90 != 0 || p99 != 0 {
		t.Errorf("expected (0,0,0) for empty full-population, got (%.2f, %.2f, %.2f)", p50, p90, p99)
	}
}

// TestRollingWindowEvictsStale verifies that Percentiles() excludes samples
// older than WindowSize from the rolling computation.
func TestRollingWindowEvictsStale(t *testing.T) {
	w := &LatencyWindow{
		WindowSize:     60 * time.Second,
		UpdateInterval: 5 * time.Second,
	}

	now := time.Now()

	// Add an old sample (70 seconds ago — outside the 60-second window).
	oldTime := now.Add(-70 * time.Second)
	send, recv := nsLatency(oldTime, 999) // extreme latency
	w.Add(oldTime, send, recv)

	// Add a recent sample (10 seconds ago — inside the window).
	recentTime := now.Add(-10 * time.Second)
	send2, recv2 := nsLatency(recentTime, 5)
	w.Add(recentTime, send2, recv2)

	// Percentiles() is rolling — should see only the recent sample.
	p50, _, _ := w.Percentiles()
	if p50 != 5 {
		t.Errorf("rolling p50: got %.1f, want 5 (stale sample should be evicted)", p50)
	}

	// FullPopulationPercentiles() covers all samples ever added.
	fp50, _, fp99 := w.FullPopulationPercentiles()
	if fp50 != 5 {
		t.Errorf("full-population p50: got %.1f, want 5", fp50)
	}
	if fp99 != 999 {
		t.Errorf("full-population p99: got %.1f, want 999", fp99)
	}
}

// TestSingleSample verifies percentiles with a single sample.
func TestSingleSample(t *testing.T) {
	w := NewLatencyWindow()
	send, recv := nsLatency(baseTime, 42)
	w.Add(baseTime, send, recv)

	p50, p90, p99 := w.FullPopulationPercentiles()
	if p50 != 42 || p90 != 42 || p99 != 42 {
		t.Errorf("single-sample: got (%.1f, %.1f, %.1f), want (42, 42, 42)", p50, p90, p99)
	}
}

// TestNegativeLatencyClampedToZero verifies that clock-skew artefacts
// (recvNs < sendNs) produce a latency of 0 rather than a negative value.
func TestNegativeLatencyClampedToZero(t *testing.T) {
	w := NewLatencyWindow()
	sendNs := int64(2_000_000_000)
	recvNs := int64(1_000_000_000) // earlier than send — clock skew
	w.Add(baseTime, sendNs, recvNs)

	p50, _, _ := w.FullPopulationPercentiles()
	if p50 != 0 {
		t.Errorf("negative latency should clamp to 0, got %.2f", p50)
	}
}

// TestPercentilesOnHundredSamples is a broader smoke test.
func TestPercentilesOnHundredSamples(t *testing.T) {
	w := NewLatencyWindow()
	for i := 1; i <= 100; i++ {
		t0 := baseTime.Add(time.Duration(i) * time.Second)
		send, recv := nsLatency(t0, float64(i))
		w.Add(t0, send, recv)
	}
	// 100 samples: values 1..100
	// p50 → ceil(0.50*100)-1 = 49 → 50
	// p90 → ceil(0.90*100)-1 = 89 → 90
	// p99 → ceil(0.99*100)-1 = 98 → 99
	p50, p90, p99 := w.FullPopulationPercentiles()
	if p50 != 50 {
		t.Errorf("p50: got %.1f, want 50", p50)
	}
	if p90 != 90 {
		t.Errorf("p90: got %.1f, want 90", p90)
	}
	if p99 != 99 {
		t.Errorf("p99: got %.1f, want 99", p99)
	}
}
