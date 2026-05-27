// Package leaderboardv1 contains hand-written Go types that mirror the
// protobuf definitions in proto/dbhp/leaderboard/v1/leaderboard.proto.
//
// These types are used by the leaderboard gRPC server without requiring
// protoc or generated code — the gRPC service is registered using a
// hand-written adapter that serialises these types over the wire.
package leaderboardv1

import "time"

// ---------------------------------------------------------------------------
// Request / Response types (mirror proto messages)
// ---------------------------------------------------------------------------

// LeaderboardRequest carries pagination parameters for GetLeaderboard.
type LeaderboardRequest struct {
	// 1-based page number.
	Page uint32
	// Number of entries per page (max 100).
	PageSize uint32
}

// LeaderboardPage is the paginated response from GetLeaderboard.
type LeaderboardPage struct {
	Entries    []*LeaderboardEntry
	Total      uint32
	Page       uint32
	PageSize   uint32
	ComputedAt time.Time
	// ETag for HTTP-layer caching.
	ETag string
}

// LeaderboardEntry is a single ranked contestant row.
type LeaderboardEntry struct {
	Rank             uint32
	ContestantID     string
	ContestantHandle string
	CompositeScore   float64
	SpeedScore       float64
	StabilityScore   float64
	AccuracyScore    float64
	P99LatencyMs     float64
	MaxTPS           uint64
	FillAccuracyPct  float64
	RunStatus        string
	BenchmarkRunID   string
}

// ContestantScoreRequest identifies a contestant for GetContestantScore.
type ContestantScoreRequest struct {
	ContestantID string
}

// ContestantScore holds the best historical score for a contestant.
type ContestantScore struct {
	ContestantID     string
	ContestantHandle string
	// Maximum composite score across all historical Benchmark Runs.
	CompositeScore float64
	SpeedScore     float64
	StabilityScore float64
	AccuracyScore  float64
	// The Benchmark Run that produced the maximum composite score.
	BestRunID  string
	ComputedAt time.Time
}

// StreamRequest carries optional filter parameters for StreamLeaderboard.
type StreamRequest struct {
	// Leave empty to receive all updates.
	ContestantID string
}

// LeaderboardUpdate is a single streamed event from StreamLeaderboard.
type LeaderboardUpdate struct {
	// Type is always "SCORE_UPDATE" for this version.
	Type      string
	Entry     *LeaderboardEntry
	Timestamp time.Time
}
