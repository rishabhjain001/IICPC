// Package handler implements the REST endpoints for the Leaderboard Service.
// (Requirements 10.8, 11.5, 11.6)
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

// LeaderboardEntry is a single ranked contestant entry in the REST response.
type LeaderboardEntry struct {
	Rank           uint32    `json:"rank"`
	ContestantID   string    `json:"contestant_id"`
	CompositeScore float64   `json:"composite_score"`
	ComputedAt     time.Time `json:"computed_at"`
}

// LeaderboardResponse is the paginated JSON envelope returned by
// GET /v1/leaderboard.
type LeaderboardResponse struct {
	Entries  []LeaderboardEntry `json:"entries"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// LeaderboardHandler serves GET /v1/leaderboard.
type LeaderboardHandler struct {
	RDB redis.Cmdable
}

// NewLeaderboardHandler creates a handler backed by the supplied Redis client.
func NewLeaderboardHandler(rdb redis.Cmdable) *LeaderboardHandler {
	return &LeaderboardHandler{RDB: rdb}
}

// ServeHTTP handles GET /v1/leaderboard?page=1&page_size=100.
//
// Fetches rankings from the leaderboard:current Redis sorted set using
// ZREVRANGEBYSCORE. Returns a paginated JSON response with an ETag header
// computed as the SHA-256 hash of the response body.
// Falls back gracefully if Redis is unavailable.
func (h *LeaderboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	page, pageSize := parsePagination(r)

	ctx := r.Context()
	entries, total, err := h.fetchFromRedis(ctx, page, pageSize)
	if err != nil {
		// Fallback: return empty response with error flag.
		// A production implementation would fall back to TimescaleDB here.
		http.Error(w, "leaderboard unavailable", http.StatusServiceUnavailable)
		return
	}

	resp := LeaderboardResponse{
		Entries:  entries,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}

	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Compute ETag as hex-encoded SHA-256 of the response body.
	sum := sha256.Sum256(body)
	etag := fmt.Sprintf(`"%x"`, sum)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// fetchFromRedis retrieves paginated leaderboard entries from the
// leaderboard:current sorted set.
func (h *LeaderboardHandler) fetchFromRedis(ctx context.Context, page, pageSize int) ([]LeaderboardEntry, int64, error) {
	const key = "leaderboard:current"

	// Total count.
	total, err := h.RDB.ZCard(ctx, key).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZCARD: %w", err)
	}

	// Zero-based offset for pagination.
	offset := int64((page - 1) * pageSize)
	limit := int64(pageSize)

	// ZREVRANGE returns members sorted by score descending.
	members, err := h.RDB.ZRevRangeWithScores(ctx, key, offset, offset+limit-1).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZREVRANGE: %w", err)
	}

	now := time.Now().UTC()
	entries := make([]LeaderboardEntry, 0, len(members))
	for i, m := range members {
		entries = append(entries, LeaderboardEntry{
			Rank:           uint32(offset) + uint32(i) + 1,
			ContestantID:   m.Member.(string),
			CompositeScore: m.Score,
			ComputedAt:     now,
		})
	}

	return entries, total, nil
}

// parsePagination extracts and validates page / page_size query parameters.
// Defaults: page=1, page_size=100; page_size is capped at 100.
func parsePagination(r *http.Request) (page, pageSize int) {
	page = 1
	pageSize = 100

	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			page = n
		}
	}
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			pageSize = n
		}
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}
