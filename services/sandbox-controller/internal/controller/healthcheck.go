package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	// defaultMaxFails is the number of consecutive health-check failures
	// required before a sandbox is marked UNHEALTHY (Requirement 3.8).
	defaultMaxFails = 3

	// defaultPollInterval is the polling period between health-check probes
	// (Requirement 3.8 — 5-second intervals).
	defaultPollInterval = 5 * time.Second

	// defaultHealthCheckPort is the port used when building the health-check
	// URL from a SandboxRef.
	defaultHealthCheckPort = 8080
)

// SandboxRef holds the runtime addressing information for a sandbox that the
// HealthChecker needs to poll.
type SandboxRef struct {
	// InternalIP is the cluster-internal IP of the sandbox pod.
	InternalIP string

	// HealthCheckPath is the HTTP path of the health endpoint (e.g. "/health").
	HealthCheckPath string

	// BenchmarkRunID identifies the benchmark run owning this sandbox.
	BenchmarkRunID string
}

// HealthChecker polls the sandbox health endpoint at PollInterval intervals.
// After MaxFails consecutive failures it calls the onUnhealthy callback and
// stops polling.
//
// Requirements: 3.8
type HealthChecker struct {
	// Client is the HTTP client used for probes.  If nil the default client
	// is used.
	Client *http.Client

	// ConsecutiveFail tracks the current run of consecutive poll failures.
	// It is reset to 0 on any successful probe.
	ConsecutiveFail int

	// MaxFails is the threshold of consecutive failures that triggers the
	// onUnhealthy callback.  Defaults to 3 if zero.
	MaxFails int

	// PollInterval is the wait between probes.  Defaults to 5 s if zero.
	PollInterval time.Duration
}

// Run polls the health endpoint of the sandbox described by ref until ctx is
// cancelled or 3 consecutive failures are detected.
//
// On 3 consecutive failures, onUnhealthy is called exactly once and Run
// returns nil.  If ctx is cancelled before that threshold is reached, Run
// returns ctx.Err().
func (h *HealthChecker) Run(ctx context.Context, ref *SandboxRef, onUnhealthy func()) error {
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
			if err := h.probe(ctx, client, ref); err != nil {
				h.ConsecutiveFail++
				if h.ConsecutiveFail >= maxFails {
					onUnhealthy()
					return nil
				}
			} else {
				// Successful probe — reset the failure counter.
				h.ConsecutiveFail = 0
			}
		}
	}
}

// probe performs a single HTTP GET against the sandbox health endpoint.
// It returns nil on a 2xx response and a non-nil error otherwise.
func (h *HealthChecker) probe(ctx context.Context, client *http.Client, ref *SandboxRef) error {
	url := fmt.Sprintf("http://%s:%d%s", ref.InternalIP, defaultHealthCheckPort, ref.HealthCheckPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("probe: build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("probe: non-2xx status %d", resp.StatusCode)
	}
	return nil
}
