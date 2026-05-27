package metrics

import (
	"sync"
	"time"
)

// TPSWindow tracks the per-second message counts in a 5-second sliding window
// and reports the peak TPS observed within that window (Requirement 7.3).
//
// Implementation: a map from Unix second (int64) to message count.  MaxTPS
// iterates over the map retaining only buckets within the last 5 seconds, so
// stale buckets are evicted lazily on each read.
//
// Thread safety: all exported methods acquire the internal Mutex.
type TPSWindow struct {
	mu      sync.Mutex
	buckets map[int64]int64 // unix_second → message count
}

// NewTPSWindow creates a TPSWindow ready for use.
func NewTPSWindow() *TPSWindow {
	return &TPSWindow{
		buckets: make(map[int64]int64),
	}
}

// Record increments the count for the second that contains ts.
// Calling Record for the same second from multiple goroutines is safe.
func (w *TPSWindow) Record(ts time.Time) {
	sec := ts.Unix()

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.buckets == nil {
		w.buckets = make(map[int64]int64)
	}
	w.buckets[sec]++
}

// MaxTPS returns the highest per-second count observed within the last 5
// seconds (inclusive of the current second).  Returns 0 if no events have
// been recorded in the window.
//
// Stale buckets (older than 5 seconds) are evicted on each call to prevent
// unbounded map growth.
func (w *TPSWindow) MaxTPS() int64 {
	now := time.Now().Unix()
	cutoff := now - 4 // keep buckets for [now-4 .. now] = 5 seconds

	w.mu.Lock()
	defer w.mu.Unlock()

	var max int64
	for sec, count := range w.buckets {
		if sec < cutoff {
			delete(w.buckets, sec)
			continue
		}
		if count > max {
			max = count
		}
	}
	return max
}
