// Package controlv1 contains hand-written Go types that mirror the protobuf
// definitions in proto/dbhp/control/v1/benchmark.proto and
// proto/dbhp/submission/v1/{contestant,submission}.proto.
//
// These types are used in place of protoc-generated code because the build
// environment does not have protoc available. The types are intentionally kept
// simple and do not implement the full proto.Message interface.
package controlv1

import "time"

// ---------------------------------------------------------------------------
// ContestantService types
// ---------------------------------------------------------------------------

// RegisterRequest is sent to create a new contestant account.
type RegisterRequest struct {
	// Handle is the unique display name (alphanumeric + underscores, 3–32 chars).
	Handle string
	// Email is the contestant's email address.
	Email string
}

// Contestant is returned after a successful registration.
type Contestant struct {
	ID        string
	Handle    string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IssueTokenRequest requests a new bearer token for the given contestant.
type IssueTokenRequest struct {
	ContestantID string
	// ExpiresAt is optional; zero value means no expiry.
	ExpiresAt time.Time
}

// TokenResponse is returned when a token is issued.
// Token is the raw bearer value — returned only at issuance, never stored.
type TokenResponse struct {
	// Token is the raw hex-encoded bearer token.
	Token     string
	// TokenID is the UUID of the token record (use for revocation).
	TokenID   string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// RevokeRequest carries the token record ID to revoke.
type RevokeRequest struct {
	TokenID string
}

// ---------------------------------------------------------------------------
// SubmissionService types
// ---------------------------------------------------------------------------

// SubmissionStatusRequest queries the current status of a submission.
type SubmissionStatusRequest struct {
	SubmissionID string
}

// SubmissionStatus is returned by GetSubmissionStatus.
type SubmissionStatus struct {
	SubmissionID string
	// Status is one of: UPLOADED, BUILT, BUILD_FAILED, BUILD_TIMEOUT,
	// BUILD_PUBLISH_FAILED, BUILD_INFRASTRUCTURE_ERROR.
	Status      string
	BuildLogURL string
	ImageDigest string
}

// BuildLogRequest requests the build log for a submission.
type BuildLogRequest struct {
	SubmissionID string
}

// BuildLogChunk is a single streamed chunk of build log data.
type BuildLogChunk struct {
	// Data is raw bytes (stdout + stderr interleaved).
	Data   []byte
	// Offset is the byte offset of this chunk within the full log.
	Offset int64
}

// ---------------------------------------------------------------------------
// BenchmarkService types
// ---------------------------------------------------------------------------

// CreateRunRequest enqueues a new Benchmark Run.
type CreateRunRequest struct {
	ContestantID string
	SubmissionID string
	// BotCount is the number of bots to provision (100–10,000). Default 1000.
	BotCount     uint32
	Distribution *ScenarioDistribution
}

// ScenarioDistribution defines percentage weights for each bot scenario.
type ScenarioDistribution struct {
	MarketMakerPct     float32
	AggressiveTakerPct float32
	CancelSpammerPct   float32
	MixedRetailPct     float32
	LatencyProberPct   float32
}

// BenchmarkRun is the canonical representation of a run's state.
type BenchmarkRun struct {
	ID           string
	ContestantID string
	SubmissionID string
	// Status follows the state machine defined in design.md §8.
	Status         string
	BotCount       uint32
	CreatedAt      time.Time
	StartedAt      time.Time
	CompletedAt    time.Time
	CompositeScore float64
	SpeedScore     float64
	StabilityScore float64
	AccuracyScore  float64
}

// GetRunRequest requests the current state of a single run.
type GetRunRequest struct {
	BenchmarkRunID string
}

// ListRunsRequest requests a paginated list of runs.
type ListRunsRequest struct {
	// Optional filter by contestant.
	ContestantID string
	// Optional exact-match filter by status.
	Status   string
	Page     uint32
	PageSize uint32
}

// BenchmarkRunList is the paginated response to ListRuns.
type BenchmarkRunList struct {
	Runs     []*BenchmarkRun
	Total    uint32
	Page     uint32
	PageSize uint32
}

// CancelRunRequest requests graceful cancellation of a non-terminal run.
type CancelRunRequest struct {
	BenchmarkRunID string
	// Reason is stored in the audit trail.
	Reason string
}
