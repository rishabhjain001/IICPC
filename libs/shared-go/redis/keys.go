// Package redis defines the Redis key schema constants used across all DBHP
// services. Every key pattern is documented with its data type, expected TTL,
// and the service(s) that own or read it.
//
// Key naming convention:
//   <domain>:<sub-domain>:<identifier>
//
// Printf-style placeholders (%s / %d) are used where identifiers are dynamic.
// Callers must use fmt.Sprintf(KeyXxx, ...) to produce concrete keys.
package redis

const (
	// ---------------------------------------------------------------------------
	// Rate Limiting  (Submission Engine)
	// ---------------------------------------------------------------------------

	// KeyRateLimit is a Sorted Set of upload timestamps (score=unix_ns,
	// member=unix_ns) for a given contestant. Used to enforce the 5-per-60-min
	// rolling-window rate limit.
	//   Key type : Sorted Set
	//   TTL      : 3600 s (reset on each new upload)
	//   %s       : contestant_id (UUID string)
	KeyRateLimit = "rate_limit:%s"

	// ---------------------------------------------------------------------------
	// Telemetry Deduplication  (Telemetry Ingester)
	// ---------------------------------------------------------------------------

	// KeyDedup is a String (SETEX) used to deduplicate telemetry events by
	// their composite key before metric computation and TimescaleDB insert.
	//   Key type : String
	//   TTL      : run_duration + 600 s
	//   %s, %s   : run_id, bot_id (UUID strings)
	//   %d       : seq_num (int64)
	KeyDedup = "dedup:%s:%s:%d"

	// ---------------------------------------------------------------------------
	// Aggregated Metrics Cache  (Telemetry Ingester → Leaderboard Service)
	// ---------------------------------------------------------------------------

	// KeyMetrics is a Hash containing the latest aggregated metrics for a
	// Benchmark Run. Fields: p50_us, p90_us, p99_us, max_tps, err_rate.
	//   Key type : Hash
	//   TTL      : run_duration + 3600 s
	//   %s       : run_id (UUID string)
	KeyMetrics = "metrics:%s"

	// ---------------------------------------------------------------------------
	// Score Cache  (Leaderboard Service)
	// ---------------------------------------------------------------------------

	// KeyScore is a Hash holding the composite and component scores for a
	// completed Benchmark Run. Fields: composite, speed, stability, accuracy.
	//   Key type : Hash
	//   TTL      : 7 days
	//   %s       : run_id (UUID string)
	KeyScore = "score:%s"

	// ---------------------------------------------------------------------------
	// Leaderboard  (Leaderboard Service)
	// ---------------------------------------------------------------------------

	// KeyLeaderboardCurrent is the live Sorted Set used for ranking. Members are
	// contestant_id strings; scores are their maximum composite score across all
	// Benchmark Runs.
	//   Key type : Sorted Set
	//   TTL      : live (no expiry)
	KeyLeaderboardCurrent = "leaderboard:current"

	// KeyLeaderboardSnapshot is a String (JSON-encoded paginated snapshot)
	// written before every WebSocket fan-out cycle so that late-connecting
	// browsers can fetch the full state without waiting for the next push.
	//   Key type : String (JSON)
	//   TTL      : 3600 s
	//   %d       : unix timestamp of snapshot creation
	KeyLeaderboardSnapshot = "leaderboard:snapshot:%d"

	// ---------------------------------------------------------------------------
	// Endpoint Registry Cache  (Sandbox Controller / Bot Fleet Manager)
	// ---------------------------------------------------------------------------

	// KeySessionEndpoints is a Hash caching the endpoints registered for a
	// Benchmark Run. Fields: <protocol> → JSON-encoded Endpoint struct.
	//   Key type : Hash
	//   TTL      : run_duration
	//   %s       : run_id (UUID string)
	KeySessionEndpoints = "session:%s:endpoints"

	// ---------------------------------------------------------------------------
	// Pub/Sub Channels  (Telemetry Ingester → Leaderboard Service → Frontend)
	// ---------------------------------------------------------------------------

	// PubSubMetrics is the per-run channel on which the Telemetry Ingester
	// publishes aggregated metric batches (JSON) at least once per second.
	//   %s : run_id (UUID string)
	PubSubMetrics = "pubsub:metrics:%s"

	// PubSubLeaderboard is the broadcast channel on which the Leaderboard
	// Service publishes SCORE_UPDATE events to all WebSocket fan-out goroutines.
	PubSubLeaderboard = "pubsub:leaderboard"
)
