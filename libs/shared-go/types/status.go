// Package types defines string constants for all status values used across
// DBHP services. Keeping them in a single shared package prevents typos and
// enables safe switch exhaustion checks.
package types

// ---------------------------------------------------------------------------
// Submission statuses  (submissions.status column)
// ---------------------------------------------------------------------------

const (
	// SubmissionStatusUploaded is set when the artifact has been received and
	// stored in the Artifact Registry but the build job has not yet started.
	SubmissionStatusUploaded = "UPLOADED"

	// SubmissionStatusBuilt is set when the hermetic build job succeeded and
	// the OCI image has been pushed to the Artifact Registry.
	SubmissionStatusBuilt = "BUILT"

	// SubmissionStatusBuildFailed is set when the build job exits with a
	// non-zero status code (Requirement 2.4).
	SubmissionStatusBuildFailed = "BUILD_FAILED"

	// SubmissionStatusBuildTimeout is set when the build job exceeds the
	// 10-minute TTL (Requirement 2.3).
	SubmissionStatusBuildTimeout = "BUILD_TIMEOUT"

	// SubmissionStatusBuildPublishFailed is set when the build succeeded but
	// the OCI image push to the Artifact Registry failed (Requirement 2.6).
	SubmissionStatusBuildPublishFailed = "BUILD_PUBLISH_FAILED"

	// SubmissionStatusBuildInfrastructureError is set when the build container
	// itself fails to start within 60 seconds (Requirement 2.1).
	SubmissionStatusBuildInfrastructureError = "BUILD_INFRASTRUCTURE_ERROR"
)

// ---------------------------------------------------------------------------
// Benchmark Run statuses  (benchmark_runs.status column)
// ---------------------------------------------------------------------------

const (
	// BenchmarkRunStatusQueued is the initial state after CreateRun succeeds.
	BenchmarkRunStatusQueued = "QUEUED"

	// BenchmarkRunStatusBuilding is set when the build job is dispatched.
	BenchmarkRunStatusBuilding = "BUILDING"

	// BenchmarkRunStatusDeploying is set after a successful build, while the
	// Sandbox Controller is bringing the container up.
	BenchmarkRunStatusDeploying = "DEPLOYING"

	// BenchmarkRunStatusRunning is set when the Sandbox is healthy and all
	// endpoints have been registered and validated.
	BenchmarkRunStatusRunning = "RUNNING"

	// BenchmarkRunStatusCollecting is set when the run duration has been
	// reached (or a STOP command issued) and the platform is flushing final
	// telemetry.
	BenchmarkRunStatusCollecting = "COLLECTING"

	// BenchmarkRunStatusScoring is set when telemetry has been flushed and
	// the Leaderboard Service is computing the composite score.
	BenchmarkRunStatusScoring = "SCORING"

	// BenchmarkRunStatusComplete is the successful terminal state.
	BenchmarkRunStatusComplete = "COMPLETE"

	// --- Terminal failure states ---

	// BenchmarkRunStatusBuildFailed mirrors the submission build failure.
	BenchmarkRunStatusBuildFailed = "BUILD_FAILED"

	// BenchmarkRunStatusBuildTimeout mirrors the submission build timeout.
	BenchmarkRunStatusBuildTimeout = "BUILD_TIMEOUT"

	// BenchmarkRunStatusSandboxCrash is set when the Sandbox container
	// terminates unexpectedly (Requirement 3.6).
	BenchmarkRunStatusSandboxCrash = "SANDBOX_CRASH"

	// BenchmarkRunStatusNetworkSetupFailed is set when the isolated overlay
	// network cannot be created (Requirement 3.4).
	BenchmarkRunStatusNetworkSetupFailed = "NETWORK_SETUP_FAILED"

	// BenchmarkRunStatusNoEndpoints is set when all Sandbox endpoints are
	// UNAVAILABLE or none are registered (Requirement 4.6).
	BenchmarkRunStatusNoEndpoints = "NO_ENDPOINTS"

	// BenchmarkRunStatusProvisioningTimeout is set when the bot fleet cannot
	// reach READY within the time limit (Requirement 5.3).
	BenchmarkRunStatusProvisioningTimeout = "PROVISIONING_TIMEOUT"

	// BenchmarkRunStatusInsufficientCapacity is set when a worker-node failure
	// displaces bots and no healthy node has capacity (Requirement 5.7).
	BenchmarkRunStatusInsufficientCapacity = "INSUFFICIENT_CAPACITY"

	// BenchmarkRunStatusCollectionTimeout is set when final metric collection
	// exceeds the 60-second deadline (Requirement 3.7).
	BenchmarkRunStatusCollectionTimeout = "COLLECTION_TIMEOUT"

	// BenchmarkRunStatusFailed is the generic terminal failure state used for
	// scoring errors and unclassified failures.
	BenchmarkRunStatusFailed = "FAILED"
)

// TerminalBenchmarkRunStatuses is the set of statuses from which a Benchmark
// Run cannot transition further. Used by the Control Plane to enforce the
// 3-concurrent-run limit (Requirement 11.4).
var TerminalBenchmarkRunStatuses = map[string]bool{
	BenchmarkRunStatusComplete:             true,
	BenchmarkRunStatusBuildFailed:          true,
	BenchmarkRunStatusBuildTimeout:         true,
	BenchmarkRunStatusSandboxCrash:         true,
	BenchmarkRunStatusNetworkSetupFailed:   true,
	BenchmarkRunStatusNoEndpoints:          true,
	BenchmarkRunStatusProvisioningTimeout:  true,
	BenchmarkRunStatusInsufficientCapacity: true,
	BenchmarkRunStatusCollectionTimeout:    true,
	BenchmarkRunStatusFailed:               true,
}

// IsTerminal returns true if the given Benchmark Run status is a terminal
// (non-progressing) state.
func IsTerminal(status string) bool {
	return TerminalBenchmarkRunStatuses[status]
}

// ---------------------------------------------------------------------------
// Endpoint statuses  (Sandbox Controller endpoint registry)
// ---------------------------------------------------------------------------

const (
	// EndpointStatusAvailable indicates that the endpoint accepted a TCP
	// connection within the 15-second validation window.
	EndpointStatusAvailable = "AVAILABLE"

	// EndpointStatusUnavailable indicates that the endpoint failed TCP
	// validation or has since become unreachable.
	EndpointStatusUnavailable = "UNAVAILABLE"
)
