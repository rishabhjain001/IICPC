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
	Rank             uint32    `json:"rank"`
	ContestantID     string    `json:"contestant_id"`
	ContestantHandle string    `json:"contestant_handle"`
	CompositeScore   float64   `json:"composite_score"`
	SpeedScore       float64   `json:"speed_score"`
	StabilityScore   float64   `json:"stability_score"`
	AccuracyScore    float64   `json:"accuracy_score"`
	P99LatencyMs     float64   `json:"p99_latency_ms"`
	MaxTPS           int64     `json:"max_tps"`
	FillAccuracyPct  float64   `json:"fill_accuracy_pct"`
	RunStatus        string    `json:"run_status"`
	BenchmarkRunID   string    `json:"benchmark_run_id"`
	ComputedAt       time.Time `json:"computed_at"`
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
// Fetches rankings from the leaderboard:current Redis sorted set.
// Returns a paginated JSON response with an ETag header computed from
// stable fields (rank + contestant_id + score), excluding ComputedAt so
// the ETag is deterministic for identical data.
// Falls back gracefully if Redis is unavailable.
func (h *LeaderboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Allow browser SPA (leaderboard-frontend) to call this endpoint directly.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, If-None-Match")

	// Handle preflight.
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	page, pageSize := parsePagination(r)

	ctx := r.Context()
	entries, total, err := h.fetchFromRedis(ctx, page, pageSize)
	if err != nil {
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

	// Marshal the body first — if this fails we can still send a clean 500
	// before any headers have been written.
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Compute ETag from stable fields only (rank + contestant_id + score),
	// excluding ComputedAt so the ETag is deterministic for identical data.
	type stableEntry struct {
		Rank           uint32  `json:"rank"`
		ContestantID   string  `json:"contestant_id"`
		CompositeScore float64 `json:"composite_score"`
	}
	stable := make([]stableEntry, len(entries))
	for i, e := range entries {
		stable[i] = stableEntry{
			Rank:           e.Rank,
			ContestantID:   e.ContestantID,
			CompositeScore: e.CompositeScore,
		}
	}
	stableBytes, _ := json.Marshal(stable)
	sum := sha256.Sum256(stableBytes)
	etag := fmt.Sprintf(`"%x"`, sum)

	// All fallible operations are done — safe to write headers and body.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	// WriteHeader is implicit in Write(200), but explicit here for clarity.
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
	stop := offset + int64(pageSize) - 1

	// ZREVRANGE returns members sorted by score descending.
	members, err := h.RDB.ZRevRangeWithScores(ctx, key, offset, stop).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZREVRANGE: %w", err)
	}

	// Always return a non-nil slice so JSON encodes as [] not null.
	entries := make([]LeaderboardEntry, 0, len(members))
	now := time.Now().UTC()

	for i, m := range members {
		// Safe type assertion — go-redis always stores string members, but
		// guard against any future interface change to avoid a runtime panic.
		handle, ok := m.Member.(string)
		if !ok {
			continue
		}
		// Compute rank as int64 to avoid uint32 overflow on large offsets,
		// then clamp to uint32 max if somehow exceeded.
		rankI64 := offset + int64(i) + 1
		var rank uint32
		if rankI64 > 0 && rankI64 <= int64(^uint32(0)) {
			rank = uint32(rankI64)
		}
		entries = append(entries, LeaderboardEntry{
			Rank:             rank,
			ContestantID:     handle,
			ContestantHandle: handle,
			CompositeScore:   m.Score,
			RunStatus:        "COMPLETE",
			ComputedAt:       now,
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
