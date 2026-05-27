// Package heartbeat implements a worker-node heartbeat monitor for the Bot
// Fleet Manager. It detects missed heartbeats and invokes a callback to
// trigger bot redistribution per Requirement 5.7.
package heartbeat

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Monitor watches worker-node heartbeats and triggers redistribution on failure.
type Monitor struct {
	// HeartbeatInterval is how often the monitor checks for missed heartbeats.
	// Default: 5 s.
	HeartbeatInterval time.Duration

	// MissedThreshold is the maximum time since the last heartbeat before a
	// node is considered failed. Default: 15 s.
	MissedThreshold time.Duration

	Logger *zap.Logger

	mu         sync.RWMutex
	lastSeen   map[string]time.Time // nodeName → last heartbeat time
}

// NewMonitor creates a Monitor with the standard intervals from Requirement 5.7.
func NewMonitor(logger *zap.Logger) *Monitor {
	return &Monitor{
		HeartbeatInterval: 5 * time.Second,
		MissedThreshold:   15 * time.Second,
		Logger:            logger,
		lastSeen:          make(map[string]time.Time),
	}
}

// RecordHeartbeat records a heartbeat received from nodeName.
func (m *Monitor) RecordHeartbeat(nodeName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastSeen == nil {
		m.lastSeen = make(map[string]time.Time)
	}
	m.lastSeen[nodeName] = time.Now()
}

// Run starts the heartbeat monitoring loop.
//
// It checks for missed heartbeats every HeartbeatInterval. When any node has
// not sent a heartbeat for longer than MissedThreshold, onNodeFailure is called
// with the node name.
//
// The node is removed from the tracking map after the failure callback fires so
// that the callback is not called repeatedly for the same failure unless a new
// heartbeat is later registered (node comes back) and then goes missing again.
//
// Run blocks until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context, onNodeFailure func(nodeName string)) {
	interval := m.HeartbeatInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	threshold := m.MissedThreshold
	if threshold <= 0 {
		threshold = 15 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkHeartbeats(threshold, onNodeFailure)
		}
	}
}

// checkHeartbeats inspects all tracked nodes and fires onNodeFailure for any
// whose last heartbeat was more than threshold ago.
func (m *Monitor) checkHeartbeats(threshold time.Duration, onNodeFailure func(nodeName string)) {
	now := time.Now()

	m.mu.Lock()
	var failed []string
	for node, last := range m.lastSeen {
		if now.Sub(last) > threshold {
			failed = append(failed, node)
		}
	}
	// Remove failed nodes so we don't call the callback again.
	for _, node := range failed {
		delete(m.lastSeen, node)
	}
	m.mu.Unlock()

	for _, node := range failed {
		m.Logger.Warn("worker node heartbeat missed, triggering redistribution",
			zap.String("node", node),
		)
		onNodeFailure(node)
	}
}

// KnownNodes returns the set of node names currently being tracked.
// Primarily useful for testing.
func (m *Monitor) KnownNodes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	nodes := make([]string, 0, len(m.lastSeen))
	for n := range m.lastSeen {
		nodes = append(nodes, n)
	}
	return nodes
}
