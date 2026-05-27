package metrics

import (
	"testing"
	"time"
)

// TestMaxTPSEmptyWindow verifies that MaxTPS returns 0 when no events have
// been recorded (Requirement 7.3).
func TestMaxTPSEmptyWindow(t *testing.T) {
	w := NewTPSWindow()
	if got := w.MaxTPS(); got != 0 {
		t.Errorf("MaxTPS for empty window: got %d, want 0", got)
	}
}

// TestMaxTPSReturnsCorrectPeak verifies that MaxTPS returns the highest
// per-second count observed in the 5-second sliding window.
func TestMaxTPSReturnsCorrectPeak(t *testing.T) {
	w := NewTPSWindow()
	now := time.Now()

	// Record 3 events in the same second.
	for i := 0; i < 3; i++ {
		w.Record(now)
	}
	// Record 10 events in the previous second (still within the 5-second window).
	prev := now.Add(-1 * time.Second)
	for i := 0; i < 10; i++ {
		w.Record(prev)
	}
	// Record 1 event in a second 2 seconds ago.
	twoAgo := now.Add(-2 * time.Second)
	w.Record(twoAgo)

	got := w.MaxTPS()
	if got != 10 {
		t.Errorf("MaxTPS: got %d, want 10 (peak was 10 events/s)", got)
	}
}

// TestMaxTPSEvictsStaleBuckets verifies that buckets older than 5 seconds are
// not included in MaxTPS.
func TestMaxTPSEvictsStaleBuckets(t *testing.T) {
	w := NewTPSWindow()
	now := time.Now()

	// Record many events 10 seconds ago (outside the 5-second window).
	old := now.Add(-10 * time.Second)
	for i := 0; i < 999; i++ {
		w.Record(old)
	}

	// Record 1 event in the current second.
	w.Record(now)

	got := w.MaxTPS()
	// The old bucket should be evicted; max should be 1 (the current second).
	if got != 1 {
		t.Errorf("MaxTPS should ignore stale buckets: got %d, want 1", got)
	}
}

// TestMaxTPSSingleSecond verifies that recording N events in the same second
// reports MaxTPS = N.
func TestMaxTPSSingleSecond(t *testing.T) {
	w := NewTPSWindow()
	now := time.Now()
	const n = 500
	for i := 0; i < n; i++ {
		w.Record(now)
	}
	got := w.MaxTPS()
	if got != n {
		t.Errorf("MaxTPS: got %d, want %d", got, n)
	}
}

// TestMaxTPSWindowEdgeBoundary verifies that events exactly at the boundary
// of the 5-second window (4 seconds ago) are still included.
func TestMaxTPSWindowEdgeBoundary(t *testing.T) {
	w := NewTPSWindow()
	now := time.Now()

	// 4 seconds ago is within the window [now-4 .. now].
	boundary := now.Add(-4 * time.Second)
	for i := 0; i < 7; i++ {
		w.Record(boundary)
	}
	w.Record(now) // 1 event in current second

	got := w.MaxTPS()
	if got != 7 {
		t.Errorf("boundary bucket should be included: got %d, want 7", got)
	}
}
