package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/iicpc/dbhp/leaderboard-service/internal/handler"
)

// ---------------------------------------------------------------------------
// In-memory Redis mock (satisfies the subset of redis.Cmdable used by handler)
// ---------------------------------------------------------------------------

type mockRedis struct {
	// member → score, in insertion order for determinism
	members []redis.Z
	// Inject an error to simulate Redis unavailability.
	err error
	// embed to satisfy the full redis.Cmdable interface; panics on unused methods
	redis.Cmdable
}

func (m *mockRedis) ZCard(ctx context.Context, key string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx)
	if m.err != nil {
		cmd.SetErr(m.err)
		return cmd
	}
	cmd.SetVal(int64(len(m.members)))
	return cmd
}

func (m *mockRedis) ZRevRangeWithScores(ctx context.Context, key string, start, stop int64) *redis.ZSliceCmd {
	cmd := redis.NewZSliceCmd(ctx)
	if m.err != nil {
		cmd.SetErr(m.err)
		return cmd
	}
	total := int64(len(m.members))
	if start >= total {
		cmd.SetVal(nil)
		return cmd
	}
	end := stop + 1
	if end > total {
		end = total
	}
	// Reverse order: highest score first (simulate ZREVRANGE).
	reversed := make([]redis.Z, len(m.members))
	for i, z := range m.members {
		reversed[len(m.members)-1-i] = z
	}
	cmd.SetVal(reversed[start:end])
	return cmd
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func newRequest(url string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	return req
}

// TestGetLeaderboard_HTTP200 verifies a 200 response with JSON entries.
func TestGetLeaderboard_HTTP200(t *testing.T) {
	m := &mockRedis{
		members: []redis.Z{
			{Score: 0.9, Member: "contestant-1"},
			{Score: 0.7, Member: "contestant-2"},
		},
	}
	h := handler.NewLeaderboardHandler(m)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("/v1/leaderboard?page=1&page_size=10"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp handler.LeaderboardResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
	if len(resp.Entries) != 2 {
		t.Errorf("entries count = %d, want 2", len(resp.Entries))
	}
}

// TestGetLeaderboard_ETagPresent verifies the ETag header is set.
func TestGetLeaderboard_ETagPresent(t *testing.T) {
	m := &mockRedis{
		members: []redis.Z{
			{Score: 0.8, Member: "contestant-A"},
		},
	}
	h := handler.NewLeaderboardHandler(m)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("/v1/leaderboard"))

	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Error("ETag header must be present")
	}
	// ETag should be a quoted string.
	if len(etag) < 2 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Errorf("ETag = %q, expected a quoted string", etag)
	}
}

// TestGetLeaderboard_ETagDeterministic verifies two identical calls produce the
// same ETag (useful for client-side caching).
func TestGetLeaderboard_ETagDeterministic(t *testing.T) {
	m := &mockRedis{
		members: []redis.Z{
			{Score: 0.6, Member: "c1"},
			{Score: 0.4, Member: "c2"},
		},
	}
	h := handler.NewLeaderboardHandler(m)

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, newRequest("/v1/leaderboard"))

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, newRequest("/v1/leaderboard"))

	if rec1.Header().Get("ETag") != rec2.Header().Get("ETag") {
		t.Error("ETag should be deterministic across identical responses")
	}
}

// TestGetLeaderboard_FallbackOnRedisError verifies that when Redis returns an
// error the handler responds with 503 Service Unavailable (graceful degradation).
func TestGetLeaderboard_FallbackOnRedisError(t *testing.T) {
	m := &mockRedis{err: &redisConnError{}}
	h := handler.NewLeaderboardHandler(m)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("/v1/leaderboard"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 on Redis error", rec.Code)
	}
}

// TestGetLeaderboard_Pagination verifies page/page_size parameters are applied.
func TestGetLeaderboard_Pagination(t *testing.T) {
	// Build 5 members.
	members := make([]redis.Z, 5)
	for i := range members {
		members[i] = redis.Z{Score: float64(5 - i), Member: "c" + string(rune('0'+i))}
	}
	m := &mockRedis{members: members}
	h := handler.NewLeaderboardHandler(m)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("/v1/leaderboard?page=1&page_size=2"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp handler.LeaderboardResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PageSize != 2 {
		t.Errorf("page_size = %d, want 2", resp.PageSize)
	}
	if len(resp.Entries) != 2 {
		t.Errorf("entries count = %d, want 2", len(resp.Entries))
	}
}

// TestGetLeaderboard_MethodNotAllowed verifies non-GET requests return 405.
func TestGetLeaderboard_MethodNotAllowed(t *testing.T) {
	m := &mockRedis{}
	h := handler.NewLeaderboardHandler(m)

	req, _ := http.NewRequest(http.MethodPost, "/v1/leaderboard", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Error helpers
// ---------------------------------------------------------------------------

// redisConnError simulates a Redis connection error.
type redisConnError struct{}

func (e *redisConnError) Error() string { return "redis: connection refused" }
