// Package leaderboard manages leaderboard state in Redis and PostgreSQL.
// (Requirements 9.7, 9.8)
package leaderboard

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// redisKeys matches the shared constants in libs/shared-go/redis/keys.go.
const (
	keyLeaderboardCurrent = "leaderboard:current"
	pubSubLeaderboard     = "pubsub:leaderboard"
)

// Store manages leaderboard state in Redis and PostgreSQL.
type Store struct {
	RDB  redis.Cmdable
	Pool *pgxpool.Pool // used for DB fallback reads
}

// NewStore creates a Store with the provided Redis client and optional
// PostgreSQL connection pool.
func NewStore(rdb redis.Cmdable, pool *pgxpool.Pool) *Store {
	return &Store{RDB: rdb, Pool: pool}
}

// UpdateStanding updates the leaderboard:current Redis sorted set with the
// contestant's maximum composite score across all runs.
//
// Uses ZADD GT (only increases the score, never decreases) to maintain the
// historical maximum. (Requirement 9.7)
func (s *Store) UpdateStanding(ctx context.Context, contestantID string, compositeScore float64) error {
	// ZADD leaderboard:current GT <score> <member>
	// "GT" flag: only update if the new score is greater than the existing one.
	result := s.RDB.ZAddGT(ctx, keyLeaderboardCurrent, redis.Z{
		Score:  compositeScore,
		Member: contestantID,
	})
	if err := result.Err(); err != nil {
		return fmt.Errorf("leaderboard UpdateStanding ZADD GT: %w", err)
	}
	return nil
}

// GetStanding returns the current standing score for contestantID from Redis.
// Returns 0.0 and nil if the contestant is not in the sorted set.
func (s *Store) GetStanding(ctx context.Context, contestantID string) (float64, error) {
	score, err := s.RDB.ZScore(ctx, keyLeaderboardCurrent, contestantID).Result()
	if err == redis.Nil {
		return 0.0, nil
	}
	if err != nil {
		return 0.0, fmt.Errorf("leaderboard GetStanding ZSCORE: %w", err)
	}
	return score, nil
}

// PublishScoreWithBackoff publishes payload to the pubsub:leaderboard channel.
// Retries up to 3 times with exponential backoff starting at 100 ms (100 ms,
// 200 ms, 400 ms) on any failure. (Requirement 9.8)
func (s *Store) PublishScoreWithBackoff(ctx context.Context, payload []byte) error {
	const maxAttempts = 3
	delay := 100 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("PublishScoreWithBackoff: context cancelled: %w", ctx.Err())
			case <-time.After(delay):
			}
			delay *= 2
		}

		err := s.RDB.Publish(ctx, pubSubLeaderboard, payload).Err()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("PublishScoreWithBackoff after %d attempts: %w", maxAttempts, lastErr)
}
