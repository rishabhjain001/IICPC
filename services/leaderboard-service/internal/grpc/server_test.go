package grpc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	leaderboardv1 "github.com/iicpc/dbhp/leaderboard-service/generated/leaderboard/v1"
	grpcserver "github.com/iicpc/dbhp/leaderboard-service/internal/grpc"
)

// ---------------------------------------------------------------------------
// Stub StandingStore
// ---------------------------------------------------------------------------

type stubStore struct {
	entries []*leaderboardv1.LeaderboardEntry
	total   int64
	scores  map[string]*leaderboardv1.ContestantScore
}

func (s *stubStore) GetLeaderboard(_ context.Context, page, pageSize int) ([]*leaderboardv1.LeaderboardEntry, int64, error) {
	// Simple slice-based pagination.
	offset := (page - 1) * pageSize
	if offset >= len(s.entries) {
		return nil, s.total, nil
	}
	end := offset + pageSize
	if end > len(s.entries) {
		end = len(s.entries)
	}
	return s.entries[offset:end], s.total, nil
}

func (s *stubStore) GetContestantScore(_ context.Context, contestantID string) (*leaderboardv1.ContestantScore, error) {
	if cs, ok := s.scores[contestantID]; ok {
		return cs, nil
	}
	return &leaderboardv1.ContestantScore{ContestantID: contestantID, ComputedAt: time.Now().UTC()}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeEntries(n int) []*leaderboardv1.LeaderboardEntry {
	entries := make([]*leaderboardv1.LeaderboardEntry, n)
	for i := range entries {
		entries[i] = &leaderboardv1.LeaderboardEntry{
			Rank:           uint32(i + 1),
			ContestantID:   "contestant-" + string(rune('A'+i)),
			CompositeScore: 1.0 - float64(i)*0.01,
		}
	}
	return entries
}

// ---------------------------------------------------------------------------
// Task 13.7 — Tests
// ---------------------------------------------------------------------------

// TestGetLeaderboard_ReturnsEntries verifies that GetLeaderboard returns
// the entries from the store with correct pagination metadata.
func TestGetLeaderboard_ReturnsEntries(t *testing.T) {
	entries := makeEntries(5)
	store := &stubStore{entries: entries, total: int64(len(entries))}
	updates := make(chan []byte)
	srv := grpcserver.NewServer(store, updates)

	resp, err := srv.GetLeaderboard(context.Background(), &leaderboardv1.LeaderboardRequest{
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("GetLeaderboard: %v", err)
	}
	if len(resp.Entries) != 5 {
		t.Errorf("got %d entries, want 5", len(resp.Entries))
	}
	if resp.Total != 5 {
		t.Errorf("total = %d, want 5", resp.Total)
	}
	if resp.ETag == "" {
		t.Error("ETag must not be empty")
	}
}

// TestGetLeaderboard_Pagination verifies page 2 returns the correct slice.
func TestGetLeaderboard_Pagination(t *testing.T) {
	entries := makeEntries(10)
	store := &stubStore{entries: entries, total: 10}
	updates := make(chan []byte)
	srv := grpcserver.NewServer(store, updates)

	resp, err := srv.GetLeaderboard(context.Background(), &leaderboardv1.LeaderboardRequest{
		Page:     2,
		PageSize: 3,
	})
	if err != nil {
		t.Fatalf("GetLeaderboard page 2: %v", err)
	}
	// Entries 3,4,5 (0-indexed 3..5).
	if len(resp.Entries) != 3 {
		t.Errorf("page 2 got %d entries, want 3", len(resp.Entries))
	}
}

// TestGetContestantScore_ReturnsCorrectScore verifies that the server returns
// the score stored for a known contestant.
func TestGetContestantScore_ReturnsCorrectScore(t *testing.T) {
	expected := &leaderboardv1.ContestantScore{
		ContestantID:   "c-42",
		CompositeScore: 0.876,
		ComputedAt:     time.Now().UTC(),
	}
	store := &stubStore{
		entries: nil,
		scores:  map[string]*leaderboardv1.ContestantScore{"c-42": expected},
	}
	updates := make(chan []byte)
	srv := grpcserver.NewServer(store, updates)

	got, err := srv.GetContestantScore(context.Background(), &leaderboardv1.ContestantScoreRequest{
		ContestantID: "c-42",
	})
	if err != nil {
		t.Fatalf("GetContestantScore: %v", err)
	}
	if got.ContestantID != expected.ContestantID {
		t.Errorf("contestant_id = %q, want %q", got.ContestantID, expected.ContestantID)
	}
	if got.CompositeScore != expected.CompositeScore {
		t.Errorf("composite_score = %v, want %v", got.CompositeScore, expected.CompositeScore)
	}
}

// TestGetContestantScore_UnknownContestant verifies that an unknown contestant
// returns a zero-score response without error.
func TestGetContestantScore_UnknownContestant(t *testing.T) {
	store := &stubStore{scores: map[string]*leaderboardv1.ContestantScore{}}
	updates := make(chan []byte)
	srv := grpcserver.NewServer(store, updates)

	got, err := srv.GetContestantScore(context.Background(), &leaderboardv1.ContestantScoreRequest{
		ContestantID: "no-such-contestant",
	})
	if err != nil {
		t.Fatalf("GetContestantScore unknown: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil response for unknown contestant")
	}
	if got.CompositeScore != 0.0 {
		t.Errorf("composite_score = %v, want 0.0 for unknown contestant", got.CompositeScore)
	}
}

// TestStreamLeaderboard_InitialSnapshot verifies that StreamLeaderboard delivers
// the current leaderboard entries as the first batch of updates before waiting
// for channel messages.
func TestStreamLeaderboard_InitialSnapshot(t *testing.T) {
	entries := makeEntries(3)
	store := &stubStore{entries: entries, total: 3}
	updates := make(chan []byte, 1)
	srv := grpcserver.NewServer(store, updates)

	var received []*leaderboardv1.LeaderboardUpdate

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Run stream in background; collect until ctx expires.
	done := make(chan error, 1)
	go func() {
		done <- srv.StreamLeaderboard(ctx, &leaderboardv1.StreamRequest{}, func(u *leaderboardv1.LeaderboardUpdate) error {
			received = append(received, u)
			return nil
		})
	}()

	// Wait for stream to finish (ctx timeout).
	<-done

	if len(received) != 3 {
		t.Errorf("initial snapshot sent %d updates, want 3", len(received))
	}
	for i, u := range received {
		if u.Type != "SCORE_UPDATE" {
			t.Errorf("update %d type = %q, want SCORE_UPDATE", i, u.Type)
		}
		if u.Entry == nil {
			t.Errorf("update %d has nil entry", i)
		}
	}
}

// TestStreamLeaderboard_StreamsUpdates verifies that updates published to the
// updates channel are forwarded by StreamLeaderboard after the initial snapshot.
func TestStreamLeaderboard_StreamsUpdates(t *testing.T) {
	store := &stubStore{entries: nil, total: 0}
	updates := make(chan []byte, 4)
	srv := grpcserver.NewServer(store, updates)

	// Pre-fill the channel with one update.
	payload := leaderboardv1.LeaderboardUpdate{
		Type: "SCORE_UPDATE",
		Entry: &leaderboardv1.LeaderboardEntry{
			ContestantID:   "c-1",
			CompositeScore: 0.75,
		},
		Timestamp: time.Now().UTC(),
	}
	raw, _ := json.Marshal(payload)
	updates <- raw

	var received []*leaderboardv1.LeaderboardUpdate

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.StreamLeaderboard(ctx, &leaderboardv1.StreamRequest{}, func(u *leaderboardv1.LeaderboardUpdate) error {
			received = append(received, u)
			return nil
		})
	}()
	<-done

	// Should have received exactly the one channel update (no initial snapshot entries).
	if len(received) != 1 {
		t.Errorf("got %d updates, want 1", len(received))
	}
	if len(received) > 0 && received[0].Entry.ContestantID != "c-1" {
		t.Errorf("contestant_id = %q, want c-1", received[0].Entry.ContestantID)
	}
}

// TestGetLeaderboard_NilRequest uses default pagination when req is nil.
func TestGetLeaderboard_NilRequest(t *testing.T) {
	entries := makeEntries(2)
	store := &stubStore{entries: entries, total: 2}
	updates := make(chan []byte)
	srv := grpcserver.NewServer(store, updates)

	resp, err := srv.GetLeaderboard(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetLeaderboard nil req: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}
