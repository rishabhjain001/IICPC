// Package publisher contains the MetricsPublisher that broadcasts aggregated
// telemetry metrics to downstream consumers (Leaderboard Service) via Redis
// pub/sub (Requirement 7.5).
package publisher

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Publisher is the minimal Redis interface required by MetricsPublisher.
// Using a narrow interface instead of redis.Cmdable keeps tests simple and
// avoids the need to satisfy the full (very large) Cmdable interface.
type Publisher interface {
	Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd
}

// AggregatedMetrics is the payload published on the Redis pub/sub channel for
// a given Benchmark Run.  All latency values are in microseconds.
type AggregatedMetrics struct {
	// P50LatencyUs is the 50th percentile round-trip latency in µs.
	P50LatencyUs float64 `json:"p50_latency_us"`
	// P90LatencyUs is the 90th percentile round-trip latency in µs.
	P90LatencyUs float64 `json:"p90_latency_us"`
	// P99LatencyUs is the 99th percentile round-trip latency in µs.
	P99LatencyUs float64 `json:"p99_latency_us"`
	// MaxTPS is the highest transactions-per-second observed in the current window.
	MaxTPS int64 `json:"max_tps"`
	// ErrorRate is the fraction of requests that returned an error in [0.0, 1.0].
	ErrorRate float64 `json:"error_rate"`
}

// publishedMessage is the envelope written to the Redis channel.  It wraps
// AggregatedMetrics and adds the BenchmarkRunID so consumers can correlate
// the update without needing to parse the channel name.
type publishedMessage struct {
	BenchmarkRunID string `json:"benchmark_run_id"`
	AggregatedMetrics
}

// MetricsPublisher publishes aggregated metrics to the Redis pub/sub channel
// `pubsub:metrics:{runID}` at a minimum frequency of once per second
// (Requirement 7.5).
//
// The caller is responsible for invoking Publish at the required cadence (e.g.
// from a time.Ticker).  This struct is intentionally stateless so it can be
// shared across goroutines and called concurrently for different run IDs.
type MetricsPublisher struct {
	// RDB is the Redis client.  Any type implementing the Publisher interface
	// (including *redis.Client and *redis.ClusterClient) is accepted.
	RDB Publisher
}

// channelKey returns the pub/sub channel name for a given Benchmark Run.
// Format: pubsub:metrics:{runID} (matches the Redis key table in the design doc).
func channelKey(runID string) string {
	return fmt.Sprintf("pubsub:metrics:%s", runID)
}

// Publish serializes m as JSON and publishes it to pubsub:metrics:{runID}.
//
// The message format is:
//
//	{
//	  "benchmark_run_id": "<uuid>",
//	  "p50_latency_us":   <float>,
//	  "p90_latency_us":   <float>,
//	  "p99_latency_us":   <float>,
//	  "max_tps":          <int>,
//	  "error_rate":       <float>
//	}
//
// Publish must be called at least once per second while the Benchmark Run is
// in RUNNING state (Requirement 7.5).  The caller manages the ticker.
func (p *MetricsPublisher) Publish(ctx context.Context, runID string, m AggregatedMetrics) error {
	msg := publishedMessage{
		BenchmarkRunID:    runID,
		AggregatedMetrics: m,
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("Publish: marshal: %w", err)
	}

	if err := p.RDB.Publish(ctx, channelKey(runID), payload).Err(); err != nil {
		return fmt.Errorf("Publish: redis PUBLISH: %w", err)
	}
	return nil
}
