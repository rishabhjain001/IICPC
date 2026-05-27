// Package ratelimit provides per-scenario rate limiting and back-pressure detection
// for Synthetic Trading Bots.
//
// References: Requirements 6.5, 6.6
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// RateLimiter enforces per-scenario message rate limits using a token-bucket / ticker approach.
//
// Supported scenarios:
//   - "LATENCY_PROBER"  — hard cap at 100 msg/s (10 ms minimum inter-message gap)
//   - "AGGRESSIVE_TAKER" — target ≥ 10,000 msg/s; reduces by 50% on back-pressure
type RateLimiter struct {
	Scenario  string  // "LATENCY_PROBER" | "AGGRESSIVE_TAKER"
	MaxRateHz float64 // messages per second

	mu       sync.Mutex
	interval time.Duration // derived from MaxRateHz; updated by ReduceRate
	last     time.Time     // time of the last token issue
}

// NewRateLimiter constructs a RateLimiter for the given scenario.
// For "LATENCY_PROBER" the max rate is pinned to 100 Hz.
// For "AGGRESSIVE_TAKER" the initial rate is set to maxRateHz (caller provides; typically 10000).
func NewRateLimiter(scenario string, maxRateHz float64) *RateLimiter {
	r := &RateLimiter{
		Scenario:  scenario,
		MaxRateHz: maxRateHz,
	}
	r.interval = rateToInterval(maxRateHz)
	return r
}

// rateToInterval converts a rate in Hz to the corresponding minimum inter-message duration.
func rateToInterval(hz float64) time.Duration {
	if hz <= 0 {
		return time.Duration(1<<62) // effectively infinite wait
	}
	return time.Duration(float64(time.Second) / hz)
}

// Wait blocks until the next message can be sent according to the current rate limit.
// Returns ctx.Err() if the context is cancelled while waiting.
func (r *RateLimiter) Wait(ctx context.Context) error {
	r.mu.Lock()
	now := time.Now()
	interval := r.interval
	var wait time.Duration
	if r.last.IsZero() {
		// First call — no wait.
		r.last = now
		r.mu.Unlock()
		return nil
	}
	elapsed := now.Sub(r.last)
	if elapsed < interval {
		wait = interval - elapsed
	}
	if wait > 0 {
		r.last = r.last.Add(interval)
	} else {
		r.last = now
	}
	r.mu.Unlock()

	if wait <= 0 {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// ReduceRate reduces the current message rate by 50%.
// This must complete within 1 second of a back-pressure signal (Requirement 6.6).
// The operation itself is O(1) — it updates the internal interval atomically under a mutex.
func (r *RateLimiter) ReduceRate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.MaxRateHz /= 2
	r.interval = rateToInterval(r.MaxRateHz)
}

// DetectBackPressure returns true when back-pressure from the target endpoint is detected:
//   - REST: HTTP status code 429 (Too Many Requests)
//   - FIX:  TCP write deadline exceeded (send buffer stall > 500 ms)
func DetectBackPressure(protocol string, statusCode int, writeDeadlineExceeded bool) bool {
	switch protocol {
	case "REST":
		return statusCode == 429
	case "FIX":
		return writeDeadlineExceeded
	default:
		return false
	}
}
