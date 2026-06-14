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

type LeaderboardResponse struct {
	Entries    []LeaderboardEntry `json:"entries"`
	Total      int64              `json:"total"`
	Page       int                `json:"page"`
	PageSize   int                `json:"page_size"`
	ComputedAt time.Time          `json:"computed_at"`
}

type LeaderboardHandler struct {
	RDB redis.Cmdable
}

func NewLeaderboardHandler(rdb redis.Cmdable) *LeaderboardHandler {
	return &LeaderboardHandler{RDB: rdb}
}

func (h *LeaderboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, If-None-Match")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	page, pageSize := parsePagination(r)

	entries, total, err := h.fetchFromRedis(r.Context(), page, pageSize)
	if err != nil {
		http.Error(w, "leaderboard unavailable", http.StatusServiceUnavailable)
		return
	}

	resp := LeaderboardResponse{
		Entries:    entries,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		ComputedAt: time.Now().UTC(),
	}

	// marshal first — if this blows up we can still return a clean 500
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// etag based on rank+handle+score only, not timestamps — so caching works
	type stable struct {
		Rank  uint32  `json:"rank"`
		ID    string  `json:"id"`
		Score float64 `json:"score"`
	}
	sv := make([]stable, len(entries))
	for i, e := range entries {
		sv[i] = stable{e.Rank, e.ContestantID, e.CompositeScore}
	}
	sb, _ := json.Marshal(sv)
	sum := sha256.Sum256(sb)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sum))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *LeaderboardHandler) fetchFromRedis(ctx context.Context, page, pageSize int) ([]LeaderboardEntry, int64, error) {
	const key = "leaderboard:current"

	total, err := h.RDB.ZCard(ctx, key).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZCARD: %w", err)
	}

	offset := int64((page - 1) * pageSize)
	members, err := h.RDB.ZRevRangeWithScores(ctx, key, offset, offset+int64(pageSize)-1).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("ZREVRANGE: %w", err)
	}

	entries := make([]LeaderboardEntry, 0, len(members))
	now := time.Now().UTC()

	for i, m := range members {
		handle, ok := m.Member.(string)
		if !ok {
			continue
		}

		// safe cast — offset can be large but rank rarely exceeds uint32 max
		rankI64 := offset + int64(i) + 1
		var rank uint32
		if rankI64 > 0 && rankI64 <= int64(^uint32(0)) {
			rank = uint32(rankI64)
		}

		e := LeaderboardEntry{
			Rank:             rank,
			ContestantID:     handle,
			ContestantHandle: handle,
			CompositeScore:   m.Score,
			RunStatus:        "COMPLETE",
			ComputedAt:       now,
		}

		// best-effort enrich from hash — fall through if missing
		meta, merr := h.RDB.HGetAll(ctx, "leaderboard:meta:"+handle).Result()
		if merr == nil && len(meta) > 0 {
			if v := meta["speed_score"]; v != "" {
				e.SpeedScore, _ = strconv.ParseFloat(v, 64)
			}
			if v := meta["stability_score"]; v != "" {
				e.StabilityScore, _ = strconv.ParseFloat(v, 64)
			}
			if v := meta["accuracy_score"]; v != "" {
				e.AccuracyScore, _ = strconv.ParseFloat(v, 64)
			}
			if v := meta["p99_latency_ms"]; v != "" {
				e.P99LatencyMs, _ = strconv.ParseFloat(v, 64)
			}
			if v := meta["max_tps"]; v != "" {
				e.MaxTPS, _ = strconv.ParseInt(v, 10, 64)
			}
			if v := meta["fill_accuracy_pct"]; v != "" {
				e.FillAccuracyPct, _ = strconv.ParseFloat(v, 64)
			}
			if v := meta["run_status"]; v != "" {
				e.RunStatus = v
			}
			if v := meta["benchmark_run_id"]; v != "" {
				e.BenchmarkRunID = v
			}
		}

		entries = append(entries, e)
	}

	return entries, total, nil
}

func parsePagination(r *http.Request) (page, pageSize int) {
	page, pageSize = 1, 100
	q := r.URL.Query()
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			page = n
		}
	}
	if v := q.Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			pageSize = n
		}
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return
}
