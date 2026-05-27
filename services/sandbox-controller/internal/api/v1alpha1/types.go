// Package v1alpha1 defines the CRD types for the Sandbox custom resource.
//
// Requirements: 3.1, 3.2, 3.3, 3.5
package v1alpha1

// SandboxPhase represents the lifecycle phase of a Sandbox resource.
type SandboxPhase string

const (
	// SandboxPhasePending means the Sandbox Pod has been created but is not
	// yet in the Running state.
	SandboxPhasePending SandboxPhase = "PENDING"

	// SandboxPhaseRunning means the Pod is Running and the internal IP has
	// been recorded in the status.
	SandboxPhaseRunning SandboxPhase = "RUNNING"

	// SandboxPhaseUnhealthy means 3 consecutive health-check polls have
	// failed (Requirement 3.8).
	SandboxPhaseUnhealthy SandboxPhase = "UNHEALTHY"

	// SandboxPhaseCrashed means the Pod terminated unexpectedly during a
	// Benchmark Run (Requirement 3.6).
	SandboxPhaseCrashed SandboxPhase = "CRASHED"

	// SandboxPhaseCompleted means the Sandbox reached its maximum lifetime
	// and was gracefully stopped (Requirement 3.7).
	SandboxPhaseCompleted SandboxPhase = "COMPLETED"

	// SandboxPhaseFailed means the Sandbox could not be started due to an
	// infrastructure error (e.g. Pod creation failure or network setup
	// failure — Requirement 3.4).
	SandboxPhaseFailed SandboxPhase = "FAILED"
)

// SandboxSpec is the desired state of a Sandbox resource.
type SandboxSpec struct {
	// SubmissionID is the unique ID of the contestant submission whose image
	// will be deployed inside this sandbox.
	SubmissionID string `json:"submissionId"`

	// BenchmarkRunID is the unique ID of the associated benchmark run.
	BenchmarkRunID string `json:"benchmarkRunId"`

	// CPUCores is the number of dedicated CPU cores to allocate (2–8).
	// Maps to both requests and limits so the pod gets Guaranteed QoS and
	// the kubelet CPU-manager can pin the cores via cpuset (Requirement 3.2).
	CPUCores int32 `json:"cpuCores"`

	// MemoryLimitGiB is the hard memory ceiling enforced via cgroup v2
	// memory.max (Requirement 3.3).
	MemoryLimitGiB int32 `json:"memoryLimitGiB"`

	// TmpfsSizeGiB is the maximum size of the ephemeral tmpfs volume mounted
	// at /tmp (Requirement 3.5).
	TmpfsSizeGiB int32 `json:"tmpfsSizeGiB"`

	// MaxLifetimeSeconds is the wall-clock deadline for the sandbox; the
	// controller will gracefully stop the pod when this expires (Requirement 3.7).
	MaxLifetimeSeconds int32 `json:"maxLifetimeSeconds"`

	// HealthCheckPath is the HTTP path polled every 5 seconds to detect
	// unhealthy containers. Optional — if empty, health-check polling is
	// skipped (Requirement 3.8).
	HealthCheckPath string `json:"healthCheckPath,omitempty"`

	// Protocols lists the network protocols the submission exposes (e.g.
	// "FIX", "REST", "WS"). Used by the endpoint validator (Requirement 4).
	Protocols []string `json:"protocols"`
}

// EndpointStatus describes a single protocol endpoint exposed by the sandbox.
type EndpointStatus struct {
	// Protocol is one of "FIX", "REST", or "WS".
	Protocol string `json:"protocol"`

	// Port is the TCP port on which the submission listens for this protocol.
	Port int32 `json:"port"`

	// Status is either "AVAILABLE" or "UNAVAILABLE" (Requirement 4.4).
	Status string `json:"status"`
}

// SandboxStatus is the observed state of a Sandbox resource, written back by
// the reconciler.
type SandboxStatus struct {
	// Phase is the current lifecycle phase (see SandboxPhase constants).
	Phase SandboxPhase `json:"phase"`

	// InternalIP is the Pod IP recorded once the sandbox reaches RUNNING.
	InternalIP string `json:"internalIP,omitempty"`

	// Endpoints is the list of protocol endpoints with their availability
	// status.
	Endpoints []EndpointStatus `json:"endpoints,omitempty"`

	// TerminationReason is a human-readable string explaining why the sandbox
	// left the RUNNING state (e.g. "OOMKilled", "NETWORK_SETUP_FAILED").
	TerminationReason string `json:"terminationReason,omitempty"`

	// ExitCode is the process exit code captured when the container
	// terminated (Requirement 3.6).
	ExitCode int32 `json:"exitCode,omitempty"`
}
