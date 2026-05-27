// Package store provides an in-memory fleet state store that tracks per-run
// fleet status for the Bot Fleet Manager.
package store

import (
	"sync"

	botfleetv1 "github.com/iicpc/dbhp/bot-fleet-manager/generated/botfleet/v1"
)

// FleetPhase represents the lifecycle phase of a fleet.
type FleetPhase string

const (
	PhaseScheduling FleetPhase = "SCHEDULING"
	PhaseStarting   FleetPhase = "STARTING"
	PhaseReady      FleetPhase = "READY"
	PhaseRunning    FleetPhase = "RUNNING"
	PhaseStopping   FleetPhase = "STOPPING"
	PhaseTerminated FleetPhase = "TERMINATED"
	PhaseFailed     FleetPhase = "FAILED"
	PhaseTimeout    FleetPhase = "TIMEOUT"
)

// FleetRecord holds the state of a single benchmark run's bot fleet.
type FleetRecord struct {
	BenchmarkRunID string
	Phase          FleetPhase
	TotalBots      int
	ReadyBots      int
	RunningBots    int
	ThrottlingBots int
	TerminatedBots int
	// PodNames lists the Kubernetes pod names created for this run.
	PodNames []string
}

// FleetStore is a thread-safe in-memory store of fleet records indexed by
// benchmark run ID.
type FleetStore struct {
	mu      sync.RWMutex
	records map[string]*FleetRecord
}

// NewFleetStore creates an empty FleetStore.
func NewFleetStore() *FleetStore {
	return &FleetStore{
		records: make(map[string]*FleetRecord),
	}
}

// Create initialises a new fleet record for runID. It overwrites any existing
// record for the same run ID.
func (s *FleetStore) Create(runID string, totalBots int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[runID] = &FleetRecord{
		BenchmarkRunID: runID,
		Phase:          PhaseScheduling,
		TotalBots:      totalBots,
	}
}

// Get returns the fleet record for runID. Returns nil if not found.
func (s *FleetStore) Get(runID string) *FleetRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[runID]
	if !ok {
		return nil
	}
	// Return a copy to prevent data races.
	cp := *r
	cp.PodNames = append([]string(nil), r.PodNames...)
	return &cp
}

// UpdatePhase updates the lifecycle phase for runID.
func (s *FleetStore) UpdatePhase(runID string, phase FleetPhase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[runID]; ok {
		r.Phase = phase
	}
}

// IncrementReady atomically increments the ReadyBots counter for runID.
func (s *FleetStore) IncrementReady(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[runID]; ok {
		r.ReadyBots++
	}
}

// AddPod records a pod name as belonging to runID.
func (s *FleetStore) AddPod(runID, podName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[runID]; ok {
		r.PodNames = append(r.PodNames, podName)
	}
}

// Delete removes the fleet record for runID.
func (s *FleetStore) Delete(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, runID)
}

// ToFleetStatus converts a FleetRecord to the botfleetv1.FleetStatus wire type.
func ToFleetStatus(r *FleetRecord) *botfleetv1.FleetStatus {
	if r == nil {
		return &botfleetv1.FleetStatus{}
	}
	return &botfleetv1.FleetStatus{
		BenchmarkRunID: r.BenchmarkRunID,
		TotalBots:      uint32(r.TotalBots),
		ReadyBots:      uint32(r.ReadyBots),
		RunningBots:    uint32(r.RunningBots),
		ThrottlingBots: uint32(r.ThrottlingBots),
		TerminatedBots: uint32(r.TerminatedBots),
	}
}
