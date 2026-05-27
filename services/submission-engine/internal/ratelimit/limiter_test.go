package ratelimit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/iicpc/dbhp/submission-engine/internal/ratelimit"
)

// newTestLimiter starts an in-process Redis server via miniredis and returns a
// Limiter wired to it, plus the miniredis instance for clock manipulation.
func newTestLimiter(t *testing.T) (*ratelimit.Limiter, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return ratelimit.New(rdb), mr
}

// TestAllow_FirstUploadAllowed verifies that the very first upload for a
// contestant is permitted.
func TestAllow_FirstUploadAllowed(t *testing.T) {
	limiter, _ := newTestLimiter(t)
	ctx := context.Background()
	const id = "contestant-first"

	allowed, retryAfter, err := limiter.Allow(ctx, id)
	if err != nil {
		t.Fatalf("Allow returned unexpected error: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allowed=true for first upload, got false (retryAfter=%d)", retryAfter)
	}
	if retryAfter != 0 {
		t.Fatalf("expected retryAfter=0 on allowed, got %d", retryAfter)
	}
}

// TestAllow_FirstFiveUploadsAllowed verifies that the first MaxUploads requests
// within a window are all permitted (and each call atomically records the upload).
func TestAllow_FirstFiveUploadsAllowed(t *testing.T) {
	limiter, _ := newTestLimiter(t)
	ctx := context.Background()
	const id = "contestant-aaa"

	for i := 1; i <= ratelimit.MaxUploads; i++ {
		allowed, retryAfter, err := limiter.Allow(ctx, id)
		if err != nil {
			t.Fatalf("upload %d: Allow returned unexpected error: %v", i, err)
		}
		if !allowed {
			t.Fatalf("upload %d: expected allowed=true, got false (retryAfter=%d)", i, retryAfter)
		}
	}
}

// TestAllow_SixthUploadRateLimited verifies that the (MaxUploads+1)-th upload
// within the window is rejected with a positive Retry-After value that does not
// exceed the window size.
func TestAllow_SixthUploadRateLimited(t *testing.T) {
	limiter, _ := newTestLimiter(t)
	ctx := context.Background()
	const id = "contestant-bbb"

	// Fill the window by calling Allow MaxUploads times (each call records too).
	for i := 0; i < ratelimit.MaxUploads; i++ {
		allowed, _, err := limiter.Allow(ctx, id)
		if err != nil {
			t.Fatalf("setup upload %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("setup upload %d: expected allowed=true", i+1)
		}
	}

	// The 6th request must be rate limited.
	allowed, retryAfter, err := limiter.Allow(ctx, id)
	if err != nil {
		t.Fatalf("6th Allow: unexpected error: %v", err)
	}
	if allowed {
		t.Fatal("6th Allow: expected allowed=false, got true")
	}
	if retryAfter <= 0 {
		t.Fatalf("6th Allow: expected retryAfter > 0, got %d", retryAfter)
	}
	if retryAfter > ratelimit.WindowSecs {
		t.Fatalf("6th Allow: retryAfter=%d exceeds window (%d s)", retryAfter, ratelimit.WindowSecs)
	}
}

// TestAllow_RetryAfterIsReasonable verifies that the Retry-After value is close
// to WindowSecs when all 5 uploads just happened (within the last second).
func TestAllow_RetryAfterIsReasonable(t *testing.T) {
	limiter, _ := newTestLimiter(t)
	ctx := context.Background()
	const id = "contestant-retryafter"

	for i := 0; i < ratelimit.MaxUploads; i++ {
		if _, _, err := limiter.Allow(ctx, id); err != nil {
			t.Fatalf("setup upload %d: %v", i+1, err)
		}
	}

	_, retryAfter, err := limiter.Allow(ctx, id)
	if err != nil {
		t.Fatalf("rate-limited Allow: %v", err)
	}
	// The oldest entry was at most ~1 second ago, so retryAfter ≈ WindowSecs.
	// Allow a 5-second tolerance for slow CI machines.
	const tolerance = 5
	if retryAfter < ratelimit.WindowSecs-tolerance {
		t.Fatalf("retryAfter=%d is unexpectedly small (expected ≈ %d)", retryAfter, ratelimit.WindowSecs)
	}
	if retryAfter > ratelimit.WindowSecs {
		t.Fatalf("retryAfter=%d exceeds WindowSecs=%d", retryAfter, ratelimit.WindowSecs)
	}
}

// TestAllow_AllowedAfterWindowExpiry verifies that after the window elapses
// (simulated via miniredis FastForward), uploads are permitted again.
func TestAllow_AllowedAfterWindowExpiry(t *testing.T) {
	limiter, mr := newTestLimiter(t)
	ctx := context.Background()
	const id = "contestant-ccc"

	// Fill the window.
	for i := 0; i < ratelimit.MaxUploads; i++ {
		if _, _, err := limiter.Allow(ctx, id); err != nil {
			t.Fatalf("setup Allow %d: %v", i+1, err)
		}
	}

	// Confirm rate limited.
	allowed, _, err := limiter.Allow(ctx, id)
	if err != nil {
		t.Fatalf("pre-expiry Allow: %v", err)
	}
	if allowed {
		t.Fatal("pre-expiry Allow: expected rate limited")
	}

	// Advance miniredis clock past the window so the sorted-set TTL fires and
	// existing entries fall outside the rolling window on the next call.
	mr.FastForward(time.Duration(ratelimit.WindowSecs+1) * time.Second)

	// After the window, the upload should be permitted again.
	allowed, _, err = limiter.Allow(ctx, id)
	if err != nil {
		t.Fatalf("post-expiry Allow: %v", err)
	}
	if !allowed {
		t.Fatal("post-expiry Allow: expected allowed=true after window expiry")
	}
}

// TestAllow_IndependentWindowsPerContestant verifies that rate limiting is
// scoped per contestant — one contestant being rate-limited does not affect
// another.
func TestAllow_IndependentWindowsPerContestant(t *testing.T) {
	limiter, _ := newTestLimiter(t)
	ctx := context.Background()

	// Exhaust the window for contestant A.
	for i := 0; i < ratelimit.MaxUploads; i++ {
		if _, _, err := limiter.Allow(ctx, "contestant-A"); err != nil {
			t.Fatalf("setup A upload %d: %v", i+1, err)
		}
	}

	// Contestant B should still be allowed.
	allowed, _, err := limiter.Allow(ctx, "contestant-B")
	if err != nil {
		t.Fatalf("contestant B Allow: %v", err)
	}
	if !allowed {
		t.Fatal("contestant B should be allowed while contestant A is rate-limited")
	}
}

// TestAllow_ConcurrentSafe performs a basic concurrent safety check: multiple
// goroutines calling Allow simultaneously must not corrupt the limiter state
// or return errors.  Exactly MaxUploads of the goroutines should receive
// allowed=true; the rest should be rate limited.
func TestAllow_ConcurrentSafe(t *testing.T) {
	limiter, _ := newTestLimiter(t)
	ctx := context.Background()
	const id = "contestant-concurrent"
	const goroutines = 20

	var wg sync.WaitGroup
	var allowedCount atomic.Int64
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, _, err := limiter.Allow(ctx, id)
			if err != nil {
				errs <- err
				return
			}
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent error: %v", err)
	}

	// Exactly MaxUploads goroutines should have been allowed.
	if got := allowedCount.Load(); got != ratelimit.MaxUploads {
		t.Errorf("concurrent Allow: expected %d allowed, got %d", ratelimit.MaxUploads, got)
	}
}
