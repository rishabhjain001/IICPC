// Package metrics provides rolling-window latency percentile computation and
// rolling-window TPS tracking for the Telemetry Ingester.
package metrics

import (
	"sort"
	"sync"
	"time"
)

// latencySample is a single latency observation stored in the rolling window.
type latencySample struct {
	// EventTime is the wall-clock time at which the sample was recorded.
	// Used to evict samples older than WindowSize.
	EventTime time.Time
	// LatencyUs is the round-trip latency in microseconds with sub-microsecond
	// resolution (1 µs = 1 000 ns; stored as float64 to preserve fractional µs).
	LatencyUs float64
}

// LatencyWindow maintains a rolling window of latency samples and computes
// p50/p90/p99 percentiles with 1-microsecond resolution (Requirement 7.2).
//
// The window size is 60 seconds and the computed percentiles are over all
// samples whose EventTime falls within the last WindowSize from now.  At run
// completion, FullPopulationPercentiles returns statistics over all samples
// ever added.
//
// Thread safety: all exported methods acquire the internal RWMutex.
type LatencyWindow struct {
	// WindowSize is the rolling window duration.  Defaults to 60 seconds.
	WindowSize time.Duration
	// UpdateInterval is how frequently the window is updated; informational
	// for callers — this struct itself does not schedule periodic updates.
	UpdateInterval time.Duration

	mu      sync.RWMutex
	samples []latencySample // append-only ring; eviction happens in Percentiles/Add
}

// NewLatencyWindow creates a LatencyWindow with the standard 60-second window
// and 5-second update interval (Requirement 7.2).
func NewLatencyWindow() *LatencyWindow {
	return &LatencyWindow{
		WindowSize:     60 * time.Second,
		UpdateInterval: 5 * time.Second,
	}
}

// Add records a new latency sample derived from the nanosecond send and receive
// timestamps carried in a TelemetryEvent.  eventTime should be the wall-clock
// time at which the event was processed (typically time.Now() at dispatch time).
//
// The latency is computed as (recvNs - sendNs) / 1000 to convert to
// microseconds (Requirement 7.2: 1-µs resolution).  Negative latencies
// (clock skew artefacts) are recorded as 0.
func (w *LatencyWindow) Add(eventTime time.Time, sendNs, recvNs int64) {
	latencyUs := float64(recvNs-sendNs) / 1_000.0
	if latencyUs < 0 {
		latencyUs = 0
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.samples = append(w.samples, latencySample{
		EventTime: eventTime,
		LatencyUs: latencyUs,
	})
}

// Percentiles returns the p50, p90, and p99 latencies (in microseconds) over
// all samples within the current rolling WindowSize.  Returns (0, 0, 0) if
// there are no samples in the window.
//
// Stale samples (older than WindowSize from now) are excluded from the
// computation but are NOT removed from the underlying slice; they remain
// available for FullPopulationPercentiles.  The sorted copy required for
// percentile computation is allocated locally to avoid holding the lock
// during the sort.
func (w *LatencyWindow) Percentiles() (p50, p90, p99 float64) {
	now := time.Now()
	cutoff := now.Add(-w.WindowSize)

	w.mu.RLock()
	var snapshot []float64
	for _, s := range w.samples {
		if !s.EventTime.Before(cutoff) {
			snapshot = append(snapshot, s.LatencyUs)
		}
	}
	w.mu.RUnlock()

	return computePercentiles(snapshot)
}

// FullPopulationPercentiles computes p50/p90/p99 over the entire sample history
// (including samples older than WindowSize).  Called at run completion
// (Requirement 7.2: "full population at run completion").
func (w *LatencyWindow) FullPopulationPercentiles() (p50, p90, p99 float64) {
	w.mu.RLock()
	snapshot := make([]float64, len(w.samples))
	for i, s := range w.samples {
		snapshot[i] = s.LatencyUs
	}
	w.mu.RUnlock()

	return computePercentiles(snapshot)
}

// computePercentiles sorts values in-place and returns p50, p90, p99.
// Returns (0, 0, 0) for an empty slice.
func computePercentiles(values []float64) (p50, p90, p99 float64) {
	n := len(values)
	if n == 0 {
		return 0, 0, 0
	}
	sort.Float64s(values)
	return percentileAt(values, 0.50),
		percentileAt(values, 0.90),
		percentileAt(values, 0.99)
}

// percentileAt returns the value at the given quantile using the "nearest rank"
// method.  p must be in (0, 1].
func percentileAt(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	// Ceiling of (p * n) − 1, clamped to valid indices.
	idx := int(p*float64(n)+0.9999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}
