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
// computed_at is included so the frontend can track freshness.
type LeaderboardResponse struct {
	Entries    []LeaderboardEntry `json:"entries"`
	Total      int64              `json:"total"`
	Page       int                `json:"page"`
	PageSize   int                `json:"page_size"`
	ComputedAt time.Time          `json:"computed_at"`
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
// Fetches rankings from the leaderboard:current Redis sorted set and enriches
// each entry with per-contestant metric details stored in leaderboard:meta:<handle>.
// Returns a paginated JSON response with an ETag header.
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
		http.Error(w, "leaderboard unavailable", http.StatusServiceUnavailable)
		return
	}

	now := time.Now().UTC()
	resp := LeaderboardResponse{
		Entries:    entries,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		ComputedAt: now,
	}

	// Marshal the body first so failures are handled before any headers are written.
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Compute ETag from stable fields (rank + handle + score), excluding timestamps.
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

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// fetchFromRedis retrieves paginated leaderboard entries from the
// leaderboard:current sorted set, enriched with per-contestant metadata.
func (h *LeaderboardHandler) fetchFromRedis(ctx context.Context, page, pageSize int) ([]LeaderboardEntry, int64, error) {
	const key = "leaderboard:current"

	total, err := h.RDB.ZCard(ctx, key).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZCARD: %w", err)
	}

	offset := int64((page - 1) * pageSize)
	stop := offset + int64(pageSize) - 1

	members, err := h.RDB.ZRevRangeWithScores(ctx, key, offset, stop).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZREVRANGE: %w", err)
	}

	// Always return a non-nil slice so JSON encodes as [] not null.
	entries := make([]LeaderboardEntry, 0, len(members))
	now := time.Now().UTC()

	for i, m := range members {
		handle, ok := m.Member.(string)
		if !ok {
			continue
		}

		// Compute rank safely (avoids uint32 overflow on large offsets).
		rankI64 := offset + int64(i) + 1
		var rank uint32
		if rankI64 > 0 && rankI64 <= int64(^uint32(0)) {
			rank = uint32(rankI64)
		}

		entry := LeaderboardEntry{
			Rank:             rank,
			ContestantID:     handle,
			ContestantHandle: handle,
			CompositeScore:   m.Score,
			RunStatus:        "COMPLETE",
			ComputedAt:       now,
		}

		// Enrich with per-contestant metadata stored in leaderboard:meta:<handle>.
		// This is a best-effort read — if the key is missing we still return the entry.
		metaKey := "leaderboard:meta:" + handle
		meta, metaErr := h.RDB.HGetAll(ctx, metaKey).Result()
		if metaErr == nil && len(meta) > 0 {
			if v, ok := meta["speed_score"]; ok {
				entry.SpeedScore, _ = strconv.ParseFloat(v, 64)
			}
			if v, ok := meta["stability_score"]; ok {
				entry.StabilityScore, _ = strconv.ParseFloat(v, 64)
			}
			if v, ok := meta["accuracy_score"]; ok {
				entry.AccuracyScore, _ = strconv.ParseFloat(v, 64)
			}
			if v, ok := meta["p99_latency_ms"]; ok {
				entry.P99LatencyMs, _ = strconv.ParseFloat(v, 64)
			}
			if v, ok := meta["max_tps"]; ok {
				entry.MaxTPS, _ = strconv.ParseInt(v, 10, 64)
			}
			if v, ok := meta["fill_accuracy_pct"]; ok {
				entry.FillAccuracyPct, _ = strconv.ParseFloat(v, 64)
			}
			if v, ok := meta["run_status"]; ok {
				entry.RunStatus = v
			}
			if v, ok := meta["benchmark_run_id"]; ok {
				entry.BenchmarkRunID = v
			}
		}

		entries = append(entries, entry)
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
