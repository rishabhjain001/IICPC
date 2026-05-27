package leaderboard_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/iicpc/dbhp/leaderboard-service/internal/leaderboard"
)

// ---------------------------------------------------------------------------
// Minimal in-memory Redis mock
// ---------------------------------------------------------------------------

// mockRedis implements the subset of redis.Cmdable used by Store.
// It is NOT a full mock — only the methods called by Store are implemented.
type mockRedis struct {
	// sorted set: member → score
	scores map[string]float64
	// pub/sub simulation
	publishCallCount int32
	publishError     error // if non-nil, Publish returns this error after publishFailTimes attempts
	publishFailTimes int   // number of times to return publishError before succeeding
	publishAttempts  int32

	// call timing for backoff tests
	publishTimes []time.Time
}

func newMockRedis() *mockRedis {
	return &mockRedis{scores: make(map[string]float64)}
}

// ZAddGT implements ZADD GT semantics: only update if new score > current.
func (m *mockRedis) ZAddGT(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd {
	for _, z := range members {
		member := z.Member.(string)
		if cur, ok := m.scores[member]; !ok || z.Score > cur {
			m.scores[member] = z.Score
		}
	}
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(0)
	return cmd
}

// ZScore returns the score for member; redis.Nil if absent.
func (m *mockRedis) ZScore(ctx context.Context, key, member string) *redis.FloatCmd {
	cmd := redis.NewFloatCmd(ctx)
	if score, ok := m.scores[member]; ok {
		cmd.SetVal(score)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}

// Publish simulates pub/sub publish, tracking call times and injecting errors.
func (m *mockRedis) Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd {
	n := atomic.AddInt32(&m.publishAttempts, 1)
	m.publishTimes = append(m.publishTimes, time.Now())
	atomic.AddInt32(&m.publishCallCount, 1)
	cmd := redis.NewIntCmd(ctx)
	if m.publishError != nil && int(n) <= m.publishFailTimes {
		cmd.SetErr(m.publishError)
		return cmd
	}
	cmd.SetVal(1)
	return cmd
}

// storeFromMock builds a leaderboard.Store backed by the mock via a thin
// adapter that satisfies redis.Cmdable through embedding.
type cmdableAdapter struct {
	*mockRedis
	// embed unused methods from redis.Cmdable — they panic if called
	redis.Cmdable
}

func (a *cmdableAdapter) ZAddGT(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd {
	return a.mockRedis.ZAddGT(ctx, key, members...)
}
func (a *cmdableAdapter) ZScore(ctx context.Context, key, member string) *redis.FloatCmd {
	return a.mockRedis.ZScore(ctx, key, member)
}
func (a *cmdableAdapter) Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd {
	return a.mockRedis.Publish(ctx, channel, message)
}

func newStore(m *mockRedis) *leaderboard.Store {
	return leaderboard.NewStore(&cmdableAdapter{mockRedis: m}, nil)
}

// ---------------------------------------------------------------------------
// Task 13.3 — Unit tests
// ---------------------------------------------------------------------------

func TestUpdateStanding_HistoricalMax(t *testing.T) {
	ctx := context.Background()
	m := newMockRedis()
	store := newStore(m)
	const id = "contestant-1"

	// First update: 0.5
	if err := store.UpdateStanding(ctx, id, 0.5); err != nil {
		t.Fatalf("UpdateStanding: %v", err)
	}
	if got := m.scores[id]; got != 0.5 {
		t.Fatalf("score after first update = %v, want 0.5", got)
	}

	// Lower score must NOT overwrite.
	if err := store.UpdateStanding(ctx, id, 0.3); err != nil {
		t.Fatalf("UpdateStanding: %v", err)
	}
	if got := m.scores[id]; got != 0.5 {
		t.Fatalf("score after lower update = %v, want 0.5 (historical max preserved)", got)
	}

	// Higher score MUST overwrite.
	if err := store.UpdateStanding(ctx, id, 0.8); err != nil {
		t.Fatalf("UpdateStanding: %v", err)
	}
	if got := m.scores[id]; got != 0.8 {
		t.Fatalf("score after higher update = %v, want 0.8", got)
	}
}

func TestGetStanding_NotFound(t *testing.T) {
	ctx := context.Background()
	m := newMockRedis()
	store := newStore(m)

	score, err := store.GetStanding(ctx, "unknown-contestant")
	if err != nil {
		t.Fatalf("GetStanding returned unexpected error: %v", err)
	}
	if score != 0.0 {
		t.Fatalf("GetStanding for unknown = %v, want 0.0", score)
	}
}

func TestPublishScoreWithBackoff_SucceedsFirstAttempt(t *testing.T) {
	ctx := context.Background()
	m := newMockRedis()
	store := newStore(m)

	if err := store.PublishScoreWithBackoff(ctx, []byte(`{"type":"SCORE_UPDATE"}`)); err != nil {
		t.Fatalf("PublishScoreWithBackoff: %v", err)
	}
	if got := atomic.LoadInt32(&m.publishCallCount); got != 1 {
		t.Fatalf("Publish call count = %d, want 1", got)
	}
}

func TestPublishScoreWithBackoff_RetriesOnFailure(t *testing.T) {
	ctx := context.Background()
	m := newMockRedis()
	m.publishError = errors.New("redis: connection refused")
	m.publishFailTimes = 2 // fail first 2 attempts, succeed on 3rd

	store := newStore(m)

	// Use a short backoff by running with a deadline-aware context.
	// The store uses 100ms/200ms delays, so total is ~300ms. We allow 2s.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := store.PublishScoreWithBackoff(ctx, []byte(`{}`)); err != nil {
		t.Fatalf("PublishScoreWithBackoff should succeed on 3rd attempt: %v", err)
	}
	if got := atomic.LoadInt32(&m.publishAttempts); got != 3 {
		t.Fatalf("Publish attempt count = %d, want 3", got)
	}
}

func TestPublishScoreWithBackoff_FailsAfterMaxRetries(t *testing.T) {
	ctx := context.Background()
	m := newMockRedis()
	m.publishError = errors.New("redis: connection refused")
	m.publishFailTimes = 10 // always fail

	store := newStore(m)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := store.PublishScoreWithBackoff(ctx, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error after max retries, got nil")
	}
	if got := atomic.LoadInt32(&m.publishAttempts); got != 3 {
		t.Fatalf("Publish attempt count = %d, want exactly 3", got)
	}
}

// ---------------------------------------------------------------------------
// Task 13.4 — Property test: historical maximum invariant
// Validates: Requirements 9.7
// ---------------------------------------------------------------------------

// TestUpdateStanding_HistoricalMaxProperty verifies that for any sequence of
// scores, the standing always equals the maximum score seen so far, and that
// adding a new score updates standing to max(standing, newScore).
//
// **Validates: Requirements 9.7**
func TestUpdateStanding_HistoricalMaxProperty(t *testing.T) {
	sequences := [][]float64{
		{0.1, 0.2, 0.3, 0.4, 0.5},
		{0.5, 0.4, 0.3, 0.2, 0.1},
		{0.3, 0.9, 0.1, 0.7, 0.2},
		{0.0},
		{1.0},
		{0.5, 0.5, 0.5},
		{0.0, 0.0, 1.0},
		{1.0, 0.0, 0.0},
		{0.1, 0.9, 0.5, 0.8, 0.3, 0.7},
	}

	for _, seq := range sequences {
		ctx := context.Background()
		m := newMockRedis()
		store := newStore(m)
		const id = "prop-test-contestant"

		var expectedMax float64
		for i, score := range seq {
			if err := store.UpdateStanding(ctx, id, score); err != nil {
				t.Fatalf("seq %v step %d UpdateStanding: %v", seq, i, err)
			}
			if score > expectedMax {
				expectedMax = score
			}
			// Invariant: standing = max of all scores seen so far.
			got, err := store.GetStanding(ctx, id)
			if err != nil {
				t.Fatalf("seq %v step %d GetStanding: %v", seq, i, err)
			}
			if got != expectedMax {
				t.Errorf("seq %v after %d updates: standing = %v, want %v", seq, i+1, got, expectedMax)
			}
		}

		// Property: adding cn+1 updates standing to max(standing, cn+1).
		newScore := 0.42
		prevMax := expectedMax
		if err := store.UpdateStanding(ctx, id, newScore); err != nil {
			t.Fatalf("seq %v final UpdateStanding: %v", seq, err)
		}
		wantFinal := prevMax
		if newScore > wantFinal {
			wantFinal = newScore
		}
		got, err := store.GetStanding(ctx, id)
		if err != nil {
			t.Fatalf("seq %v final GetStanding: %v", seq, err)
		}
		if got != wantFinal {
			t.Errorf("seq %v after final update: standing = %v, want %v", seq, got, wantFinal)
		}
	}
}
