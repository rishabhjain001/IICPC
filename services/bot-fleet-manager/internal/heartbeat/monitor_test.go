package heartbeat

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// newTestMonitor creates a Monitor with accelerated intervals for testing.
func newTestMonitor(checkInterval, missed time.Duration) *Monitor {
	logger, _ := zap.NewDevelopment()
	return &Monitor{
		HeartbeatInterval: checkInterval,
		MissedThreshold:   missed,
		Logger:            logger,
		lastSeen:          make(map[string]time.Time),
	}
}

// TestNodeFailureDetectedAfter15s verifies that the failure callback fires when
// a node's heartbeat has been missing for longer than MissedThreshold.
func TestNodeFailureDetectedAfter15s(t *testing.T) {
	// Use very short intervals so the test completes quickly.
	m := newTestMonitor(5*time.Millisecond, 10*time.Millisecond)

	// Register a heartbeat that is already older than the threshold.
	m.mu.Lock()
	m.lastSeen["node-stale"] = time.Now().Add(-20 * time.Millisecond)
	m.mu.Unlock()

	var (
		mu      sync.Mutex
		called  []string
	)
	onFailure := func(nodeName string) {
		mu.Lock()
		called = append(called, nodeName)
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go m.Run(ctx, onFailure)

	// Wait for the callback to fire.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		n := len(called)
		mu.Unlock()
		if n > 0 {
			break
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(called) == 0 {
		t.Error("expected onNodeFailure to be called for stale node")
		return
	}
	if called[0] != "node-stale" {
		t.Errorf("expected callback with 'node-stale', got %q", called[0])
	}
}

// TestHeartbeatResetsTimer verifies that a fresh heartbeat prevents the failure
// callback from firing (no false positives).
func TestHeartbeatResetsTimer(t *testing.T) {
	m := newTestMonitor(5*time.Millisecond, 20*time.Millisecond)

	m.RecordHeartbeat("node-healthy")

	var (
		mu     sync.Mutex
		called []string
	)
	onFailure := func(nodeName string) {
		mu.Lock()
		called = append(called, nodeName)
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	go m.Run(ctx, onFailure)

	// Keep refreshing the heartbeat faster than the missed threshold.
	go func() {
		ticker := time.NewTicker(8 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.RecordHeartbeat("node-healthy")
			}
		}
	}()

	<-ctx.Done()

	mu.Lock()
	defer mu.Unlock()
	if len(called) > 0 {
		t.Errorf("expected no failure callbacks for healthy node, got %v", called)
	}
}

// TestRedistributionCallbackCalledWithCorrectNodeName verifies that the
// callback receives the exact node name that missed its heartbeat.
func TestRedistributionCallbackCalledWithCorrectNodeName(t *testing.T) {
	m := newTestMonitor(5*time.Millisecond, 10*time.Millisecond)

	// Register stale heartbeats for two specific nodes.
	staleTime := time.Now().Add(-50 * time.Millisecond)
	m.mu.Lock()
	m.lastSeen["node-alpha"] = staleTime
	m.lastSeen["node-beta"] = staleTime
	m.mu.Unlock()

	// Also register a healthy node to ensure it is not included.
	m.RecordHeartbeat("node-gamma")

	var (
		mu     sync.Mutex
		called []string
	)
	onFailure := func(nodeName string) {
		mu.Lock()
		called = append(called, nodeName)
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	go m.Run(ctx, onFailure)

	// Keep node-gamma healthy throughout the test by refreshing its heartbeat.
	go func() {
		ticker := time.NewTicker(3 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.RecordHeartbeat("node-gamma")
			}
		}
	}()

	// Wait for callbacks for the two stale nodes.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		n := len(called)
		mu.Unlock()
		if n >= 2 {
			break
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if len(called) < 2 {
		t.Errorf("expected callbacks for both stale nodes, got %v", called)
		return
	}

	calledSet := map[string]bool{}
	for _, name := range called {
		calledSet[name] = true
	}
	if !calledSet["node-alpha"] {
		t.Errorf("expected callback for node-alpha, got %v", called)
	}
	if !calledSet["node-beta"] {
		t.Errorf("expected callback for node-beta, got %v", called)
	}
	if calledSet["node-gamma"] {
		t.Errorf("unexpected callback for healthy node-gamma")
	}
}
