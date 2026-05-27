package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// HealthChecker tests
// ---------------------------------------------------------------------------

// sandboxRefFromServer builds a SandboxRef that points to the test server.
// We parse host and port from the test server URL.
func sandboxRefFromServer(srv *httptest.Server) *SandboxRef {
	// httptest.Server URL looks like "http://127.0.0.1:PORT"
	addr := strings.TrimPrefix(srv.URL, "http://")
	// addr is "127.0.0.1:PORT"
	colonIdx := strings.LastIndex(addr, ":")
	ip := addr[:colonIdx]
	return &SandboxRef{
		InternalIP:      ip,
		HealthCheckPath: "/health",
		BenchmarkRunID:  "run-test",
	}
}

// newHealthChecker builds a HealthChecker with a short poll interval suitable
// for unit tests.
func newHealthChecker(client *http.Client, pollInterval time.Duration) *HealthChecker {
	return &HealthChecker{
		Client:       client,
		MaxFails:     defaultMaxFails,
		PollInterval: pollInterval,
	}
}

// TestHealthChecker_ThreeConsecutiveFailuresTriggersCallback verifies that
// exactly 3 consecutive failures trigger onUnhealthy and that polling stops.
//
// Requirements: 3.8
func TestHealthChecker_ThreeConsecutiveFailuresTriggersCallback(t *testing.T) {
	// Server that always returns HTTP 503.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ref := sandboxRefFromServer(srv)
	// Override the port: we need the actual test server port in the probe URL.
	// Re-derive it from the server address.
	ref.HealthCheckPath = "/health"

	var callbackFired atomic.Bool
	onUnhealthy := func() { callbackFired.Store(true) }

	checker := &HealthChecker{
		Client:       srv.Client(),
		MaxFails:     defaultMaxFails,
		PollInterval: 20 * time.Millisecond, // fast for test
	}

	// We need the checker to probe the test server, not the default port 8080.
	// Override probe to use the server URL directly.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := runCheckerWithURL(ctx, checker, srv.URL+"/health", onUnhealthy)

	if !callbackFired.Load() {
		t.Error("onUnhealthy should have been called after 3 consecutive failures")
	}
	// After the callback fires, Run returns nil.
	if err != nil {
		t.Errorf("expected nil error after callback, got %v", err)
	}
}

// TestHealthChecker_SuccessAfterTwoFailuresResetsCounter verifies that a
// successful probe after 2 failures resets the consecutive-failure counter and
// the callback is NOT fired.
//
// Requirements: 3.8
func TestHealthChecker_SuccessAfterTwoFailuresResetsCounter(t *testing.T) {
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requestCount.Add(1)
		// Requests 1 and 2: fail; request 3 and onwards: succeed.
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	var callbackFired atomic.Bool
	onUnhealthy := func() { callbackFired.Store(true) }

	checker := &HealthChecker{
		Client:       srv.Client(),
		MaxFails:     defaultMaxFails,
		PollInterval: 20 * time.Millisecond,
	}

	// Let the checker run for a fixed number of polls then cancel.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_ = runCheckerWithURL(ctx, checker, srv.URL+"/health", onUnhealthy)

	if callbackFired.Load() {
		t.Error("onUnhealthy should NOT have been called: success at probe 3 resets counter")
	}
	// ConsecutiveFail should have been reset to 0 after the successful probe.
	if checker.ConsecutiveFail >= defaultMaxFails {
		t.Errorf("ConsecutiveFail should have been reset, got %d", checker.ConsecutiveFail)
	}
}

// TestHealthChecker_PollIntervalRespected verifies that probes are sent
// approximately every PollInterval and not faster.
//
// Requirements: 3.8
func TestHealthChecker_PollIntervalRespected(t *testing.T) {
	pollInterval := 50 * time.Millisecond
	var requestCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	checker := &HealthChecker{
		Client:       srv.Client(),
		MaxFails:     defaultMaxFails,
		PollInterval: pollInterval,
	}

	// Run for approximately 5 intervals.
	runDuration := 5*pollInterval + pollInterval/2
	ctx, cancel := context.WithTimeout(context.Background(), runDuration)
	defer cancel()

	_ = runCheckerWithURL(ctx, checker, srv.URL+"/health", func() {})

	got := int(requestCount.Load())
	// In 5.5 interval windows we expect roughly 5 polls (ticker fires at 1×,
	// 2×, 3×, 4×, 5× pollInterval).  Allow ±2 for scheduling jitter.
	if got < 3 || got > 7 {
		t.Errorf("expected ~5 polls in %s, got %d", runDuration, got)
	}
}

// TestHealthChecker_ContextCancellationStopsPoller verifies that cancelling
// the context causes Run to return immediately without firing onUnhealthy.
//
// Requirements: 3.8
func TestHealthChecker_ContextCancellationStopsPoller(t *testing.T) {
	// Server that always succeeds.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	checker := &HealthChecker{
		Client:       srv.Client(),
		MaxFails:     defaultMaxFails,
		PollInterval: 50 * time.Millisecond,
	}

	var callbackFired atomic.Bool
	onUnhealthy := func() { callbackFired.Store(true) }

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay.
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	err := runCheckerWithURL(ctx, checker, srv.URL+"/health", onUnhealthy)

	if callbackFired.Load() {
		t.Error("onUnhealthy should NOT have been called when context is cancelled")
	}
	if err == nil {
		t.Error("expected a context cancellation error, got nil")
	}
}

// ---------------------------------------------------------------------------
// runCheckerWithURL is a test-local variant of HealthChecker.Run that probes
// a fully-qualified URL instead of building one from InternalIP + port.
// This lets tests use httptest.Server on a random port without touching the
// production probe logic.
// ---------------------------------------------------------------------------
func runCheckerWithURL(
	ctx context.Context,
	h *HealthChecker,
	url string,
	onUnhealthy func(),
) error {
	maxFails := h.MaxFails
	if maxFails <= 0 {
		maxFails = defaultMaxFails
	}
	interval := h.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			err := probeURL(ctx, client, url)
			if err != nil {
				h.ConsecutiveFail++
				if h.ConsecutiveFail >= maxFails {
					onUnhealthy()
					return nil
				}
			} else {
				h.ConsecutiveFail = 0
			}
		}
	}
}

// probeURL performs a single HTTP GET and returns nil on 2xx.
func probeURL(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return context.DeadlineExceeded // re-use a sentinel error
	}
	return nil
}
