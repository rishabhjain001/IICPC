// Package controller implements the core reconciliation logic for the
// Sandbox Controller Kubernetes operator.
package controller

import "time"

// SandboxPhase represents the lifecycle state of a Sandbox resource.
type SandboxPhase string

const (
	PhasePending   SandboxPhase = "PENDING"
	PhaseRunning   SandboxPhase = "RUNNING"
	PhaseUnhealthy SandboxPhase = "UNHEALTHY"
	PhaseCrashed   SandboxPhase = "CRASHED"
	PhaseCompleted SandboxPhase = "COMPLETED"
	PhaseFailed    SandboxPhase = "FAILED"
)

// BenchmarkRunStatus represents the status of a Benchmark Run reported back
// to the Control Plane.
type BenchmarkRunStatus string

const (
	RunStatusSandboxCrash       BenchmarkRunStatus = "SANDBOX_CRASH"
	RunStatusCollectionTimeout  BenchmarkRunStatus = "COLLECTION_TIMEOUT"
	RunStatusFailed             BenchmarkRunStatus = "FAILED"
)

// SandboxSpec holds the immutable configuration for a Sandbox deployment.
type SandboxSpec struct {
	SubmissionID      string
	BenchmarkRunID    string
	CPUCores          int
	MemoryLimitGiB    int
	TmpfsSizeGiB      int
	MaxLifetimeSeconds int64
	HealthCheckPath   string
}

// SandboxStatus holds the mutable runtime state of a Sandbox.
type SandboxStatus struct {
	Phase             SandboxPhase
	InternalIP        string
	TerminationReason string
	ExitCode          int32
}

// Sandbox is the in-memory representation of the Sandbox custom resource.
type Sandbox struct {
	Name      string
	Namespace string
	Spec      SandboxSpec
	Status    SandboxStatus
}

// PodEvent carries a single phase-change notification from the pod watcher.
type PodEvent struct {
	Phase     string
	Reason    string
	ExitCode  int32
	Timestamp time.Time
}

// ResourceReleaser is the interface for releasing all resources held by a
// Sandbox (CPU cores, memory cgroup, network namespace, tmpfs volume).
type ResourceReleaser interface {
	ReleaseResources(sandbox *Sandbox) error
}

// RunStatusUpdater is the interface for reporting Benchmark Run status back
// to the Control Plane.
type RunStatusUpdater interface {
	UpdateRunStatus(benchmarkRunID string, status BenchmarkRunStatus) error
}

// ResourceReleaserFunc is a convenience adapter so that a plain function can
// satisfy ResourceReleaser.
type ResourceReleaserFunc func(sandbox *Sandbox) error

func (f ResourceReleaserFunc) ReleaseResources(sandbox *Sandbox) error { return f(sandbox) }

// RunStatusUpdaterFunc is a convenience adapter so that a plain function can
// satisfy RunStatusUpdater.
type RunStatusUpdaterFunc func(benchmarkRunID string, status BenchmarkRunStatus) error

func (f RunStatusUpdaterFunc) UpdateRunStatus(benchmarkRunID string, status BenchmarkRunStatus) error {
	return f(benchmarkRunID, status)
}
