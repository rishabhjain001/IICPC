package ratelimit

import (
	"context"
	"testing"
	"time"
)

// TestLatencyProberEnforcesMaxRate verifies LATENCY_PROBER stays at or below 100 msg/s.
// We measure time for N sequential Wait() calls and check throughput.
func TestLatencyProberEnforcesMaxRate(t *testing.T) {
	const maxHz = 100.0
	const n = 10 // test with 10 messages for speed; at 100 msg/s that takes ~90ms minimum

	r := NewRateLimiter("LATENCY_PROBER", maxHz)
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < n; i++ {
		if err := r.Wait(ctx); err != nil {
			t.Fatalf("Wait() returned error: %v", err)
		}
	}
	elapsed := time.Since(start)

	// n calls at 100 msg/s means (n-1) intervals of 10ms each = at least (n-1)*10ms.
	minExpected := time.Duration(n-1) * (time.Second / maxHz)
	if elapsed < minExpected {
		t.Errorf("elapsed %v < minExpected %v — rate limit not enforced", elapsed, minExpected)
	}

	// Also check we didn't take unreasonably long (allow 5x slack for slow CI environments).
	maxExpected := minExpected * 5
	if elapsed > maxExpected {
		t.Errorf("elapsed %v > maxExpected %v — rate limiter too slow", elapsed, maxExpected)
	}
}

// TestWaitContextCancellation verifies Wait returns ctx.Err() on cancellation.
func TestWaitContextCancellation(t *testing.T) {
	// Use a very low rate (1 msg/s) so the second call will block for ~1s.
	r := NewRateLimiter("LATENCY_PROBER", 1.0)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// First call should succeed immediately.
	if err := r.Wait(ctx); err != nil {
		t.Fatalf("first Wait() returned unexpected error: %v", err)
	}
	// Second call should block and then be cancelled.
	err := r.Wait(ctx)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}

// TestBackPressureDetectionREST429 verifies HTTP 429 triggers back-pressure for REST.
func TestBackPressureDetectionREST429(t *testing.T) {
	if !DetectBackPressure("REST", 429, false) {
		t.Error("expected back-pressure detected on HTTP 429")
	}
}

// TestBackPressureDetectionREST200 verifies HTTP 200 does not trigger back-pressure.
func TestBackPressureDetectionREST200(t *testing.T) {
	if DetectBackPressure("REST", 200, false) {
		t.Error("unexpected back-pressure on HTTP 200")
	}
}

// TestBackPressureDetectionFIXWriteDeadline verifies write deadline triggers back-pressure for FIX.
func TestBackPressureDetectionFIXWriteDeadline(t *testing.T) {
	if !DetectBackPressure("FIX", 0, true) {
		t.Error("expected back-pressure detected on FIX write deadline exceeded")
	}
}

// TestBackPressureDetectionFIXNoDeadline verifies no back-pressure when FIX write is fine.
func TestBackPressureDetectionFIXNoDeadline(t *testing.T) {
	if DetectBackPressure("FIX", 0, false) {
		t.Error("unexpected back-pressure on FIX with writeDeadlineExceeded=false")
	}
}

// TestReduceRateHalvesRate verifies ReduceRate halves the current rate.
func TestReduceRateHalvesRate(t *testing.T) {
	const initial = 10000.0
	r := NewRateLimiter("AGGRESSIVE_TAKER", initial)

	before := r.MaxRateHz
	r.ReduceRate()
	after := r.MaxRateHz

	if after != before/2 {
		t.Fatalf("expected MaxRateHz=%g after ReduceRate, got %g", before/2, after)
	}
}

// TestReduceRateUpdatesInterval verifies the internal interval is recalculated after ReduceRate.
func TestReduceRateUpdatesInterval(t *testing.T) {
	r := NewRateLimiter("AGGRESSIVE_TAKER", 10000.0)
	beforeInterval := r.interval
	r.ReduceRate()
	afterInterval := r.interval
	// After halving rate, interval should double.
	if afterInterval != beforeInterval*2 {
		t.Fatalf("expected interval %v after ReduceRate, got %v", beforeInterval*2, afterInterval)
	}
}

// TestReduceRateCompletesWithinOneSecond verifies ReduceRate completes within 1 second (Req 6.6).
func TestReduceRateCompletesWithinOneSecond(t *testing.T) {
	r := NewRateLimiter("AGGRESSIVE_TAKER", 10000.0)
	start := time.Now()
	r.ReduceRate()
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("ReduceRate took %v, must complete within 1 second", elapsed)
	}
}

// TestReduceRateMultipleTimes verifies ReduceRate can be called multiple times.
func TestReduceRateMultipleTimes(t *testing.T) {
	r := NewRateLimiter("AGGRESSIVE_TAKER", 10000.0)
	r.ReduceRate() // 5000
	r.ReduceRate() // 2500
	r.ReduceRate() // 1250
	expected := 10000.0 / 8.0
	if r.MaxRateHz != expected {
		t.Fatalf("expected %g after 3 ReduceRate calls, got %g", expected, r.MaxRateHz)
	}
}

// TestNewRateLimiterSetsCorrectInterval verifies the initial interval matches MaxRateHz.
func TestNewRateLimiterSetsCorrectInterval(t *testing.T) {
	r := NewRateLimiter("LATENCY_PROBER", 100.0)
	expected := 10 * time.Millisecond
	if r.interval != expected {
		t.Fatalf("expected interval %v for 100 Hz, got %v", expected, r.interval)
	}
}

// TestAggressiveTakerInitialRate verifies AGGRESSIVE_TAKER is initialized with provided rate.
func TestAggressiveTakerInitialRate(t *testing.T) {
	r := NewRateLimiter("AGGRESSIVE_TAKER", 10000.0)
	if r.MaxRateHz != 10000.0 {
		t.Fatalf("expected MaxRateHz=10000, got %g", r.MaxRateHz)
	}
	expected := 100 * time.Microsecond
	if r.interval != expected {
		t.Fatalf("expected interval %v for 10000 Hz, got %v", expected, r.interval)
	}
}
