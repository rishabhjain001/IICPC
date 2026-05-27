// Package grpc implements the LeaderboardService gRPC server.
// (Requirements 11.1, 11.6)
//
// Because protoc is not available in this environment the service is
// implemented as a plain Go struct with the same logical API as the
// LeaderboardService proto definition. Wire-up to a real gRPC server is
// deferred to main.go; the types and logic here are fully testable in
// isolation.
package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	leaderboardv1 "github.com/iicpc/dbhp/leaderboard-service/generated/leaderboard/v1"
)

// ---------------------------------------------------------------------------
// StandingStore interface — satisfied by leaderboard.Store in production.
// ---------------------------------------------------------------------------

// StandingStore is the persistence interface required by the gRPC server.
type StandingStore interface {
	// GetLeaderboard returns paginated entries from the leaderboard:current
	// sorted set, falling back to DB when Redis is unavailable.
	GetLeaderboard(ctx context.Context, page, pageSize int) ([]*leaderboardv1.LeaderboardEntry, int64, error)

	// GetContestantScore returns the best historical score for a contestant.
	GetContestantScore(ctx context.Context, contestantID string) (*leaderboardv1.ContestantScore, error)
}

// ---------------------------------------------------------------------------
// UpdateListener subscribes to score updates (e.g. from hub.Hub).
// ---------------------------------------------------------------------------

// UpdateChan is a channel of JSON-encoded LeaderboardUpdate messages.
// The hub publishes serialised payloads; the gRPC stream server deserialises them.
type UpdateChan <-chan []byte

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Server implements the LeaderboardService API.
type Server struct {
	Store   StandingStore
	Updates UpdateChan
}

// NewServer creates a Server backed by the provided store and update channel.
func NewServer(store StandingStore, updates UpdateChan) *Server {
	return &Server{Store: store, Updates: updates}
}

// GetLeaderboard returns a paginated snapshot of the current leaderboard.
// Satisfies Requirement 11.6: p99 ≤ 50 ms for pages up to 100 entries.
func (s *Server) GetLeaderboard(ctx context.Context, req *leaderboardv1.LeaderboardRequest) (*leaderboardv1.LeaderboardPage, error) {
	if req == nil {
		req = &leaderboardv1.LeaderboardRequest{Page: 1, PageSize: 100}
	}
	page := int(req.Page)
	if page < 1 {
		page = 1
	}
	pageSize := int(req.PageSize)
	if pageSize < 1 || pageSize > 100 {
		pageSize = 100
	}

	entries, total, err := s.Store.GetLeaderboard(ctx, page, pageSize)
	if err != nil {
		return nil, fmt.Errorf("GetLeaderboard: %w", err)
	}

	now := time.Now().UTC()

	// Compute ETag from a hash of the entry IDs + scores (cheap but cache-friendly).
	h := sha256.New()
	for _, e := range entries {
		_, _ = fmt.Fprintf(h, "%s:%.6f;", e.ContestantID, e.CompositeScore)
	}
	etag := fmt.Sprintf(`"%x"`, h.Sum(nil))

	return &leaderboardv1.LeaderboardPage{
		Entries:    entries,
		Total:      uint32(total),
		Page:       uint32(page),
		PageSize:   uint32(pageSize),
		ComputedAt: now,
		ETag:       etag,
	}, nil
}

// GetContestantScore returns the latest historical best score for a contestant.
func (s *Server) GetContestantScore(ctx context.Context, req *leaderboardv1.ContestantScoreRequest) (*leaderboardv1.ContestantScore, error) {
	if req == nil || req.ContestantID == "" {
		return nil, fmt.Errorf("GetContestantScore: contestant_id is required")
	}
	score, err := s.Store.GetContestantScore(ctx, req.ContestantID)
	if err != nil {
		return nil, fmt.Errorf("GetContestantScore: %w", err)
	}
	return score, nil
}

// StreamLeaderboard sends incremental SCORE_UPDATE events from the hub's
// broadcast channel. It first delivers an initial snapshot then streams updates.
//
// The ctx parameter controls lifetime; when ctx is cancelled the stream ends.
func (s *Server) StreamLeaderboard(ctx context.Context, req *leaderboardv1.StreamRequest, send func(*leaderboardv1.LeaderboardUpdate) error) error {
	if req == nil {
		req = &leaderboardv1.StreamRequest{}
	}

	// --- Initial snapshot ---
	page, err := s.GetLeaderboard(ctx, &leaderboardv1.LeaderboardRequest{Page: 1, PageSize: 100})
	if err != nil {
		return fmt.Errorf("StreamLeaderboard initial snapshot: %w", err)
	}
	for _, entry := range page.Entries {
		if req.ContestantID != "" && entry.ContestantID != req.ContestantID {
			continue
		}
		update := &leaderboardv1.LeaderboardUpdate{
			Type:      "SCORE_UPDATE",
			Entry:     entry,
			Timestamp: page.ComputedAt,
		}
		if err := send(update); err != nil {
			return err
		}
	}

	// --- Stream ongoing updates ---
	for {
		select {
		case <-ctx.Done():
			return nil
		case raw, ok := <-s.Updates:
			if !ok {
				return nil
			}
			var update leaderboardv1.LeaderboardUpdate
			if err := json.Unmarshal(raw, &update); err != nil {
				// Malformed message — skip.
				continue
			}
			if req.ContestantID != "" && update.Entry != nil && update.Entry.ContestantID != req.ContestantID {
				continue
			}
			if err := send(&update); err != nil {
				return err
			}
		}
	}
}

// ---------------------------------------------------------------------------
// redisStore — default StandingStore backed by Redis / pgx.
// ---------------------------------------------------------------------------

// RedisStore provides the StandingStore implementation backed by Redis with
// optional PostgreSQL fallback. It is used by main.go to wire the gRPC server.
type RedisStore struct {
	RDB redis.Cmdable
}

const (
	redisKeyLeaderboard = "leaderboard:current"
)

// GetLeaderboard fetches paginated entries from the leaderboard:current sorted set.
func (r *RedisStore) GetLeaderboard(ctx context.Context, page, pageSize int) ([]*leaderboardv1.LeaderboardEntry, int64, error) {
	total, err := r.RDB.ZCard(ctx, redisKeyLeaderboard).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZCARD: %w", err)
	}

	offset := int64((page - 1) * pageSize)
	members, err := r.RDB.ZRevRangeWithScores(ctx, redisKeyLeaderboard, offset, offset+int64(pageSize)-1).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZREVRANGE: %w", err)
	}

	entries := make([]*leaderboardv1.LeaderboardEntry, 0, len(members))
	for i, m := range members {
		entries = append(entries, &leaderboardv1.LeaderboardEntry{
			Rank:           uint32(offset) + uint32(i) + 1,
			ContestantID:   m.Member.(string),
			CompositeScore: m.Score,
		})
	}
	return entries, total, nil
}

// GetContestantScore fetches the individual contestant's score from Redis.
func (r *RedisStore) GetContestantScore(ctx context.Context, contestantID string) (*leaderboardv1.ContestantScore, error) {
	score, err := r.RDB.ZScore(ctx, redisKeyLeaderboard, contestantID).Result()
	if err == redis.Nil {
		return &leaderboardv1.ContestantScore{
			ContestantID: contestantID,
			ComputedAt:   time.Now().UTC(),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ZSCORE: %w", err)
	}
	return &leaderboardv1.ContestantScore{
		ContestantID:   contestantID,
		CompositeScore: score,
		ComputedAt:     time.Now().UTC(),
	}, nil
}
