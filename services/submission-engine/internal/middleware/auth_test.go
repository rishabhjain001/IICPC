package middleware_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/iicpc/dbhp/submission-engine/internal/db"
	"github.com/iicpc/dbhp/submission-engine/internal/middleware"
)

// -------------------------------------------------------------------
// Stub Querier
// -------------------------------------------------------------------

// stubQuerier is a test double for db.Querier. It maps token hashes
// (as hex-comparable byte slices) to contestant IDs, or returns errors.
type stubQuerier struct {
	// tokens maps SHA-256(rawToken) → contestantID
	tokens map[[32]byte]string
}

func (s *stubQuerier) LookupContestantByTokenHash(_ context.Context, tokenHash []byte) (string, error) {
	if len(tokenHash) != 32 {
		return "", errors.New("invalid hash length")
	}
	var key [32]byte
	copy(key[:], tokenHash)
	id, ok := s.tokens[key]
	if !ok {
		return "", pgx.ErrNoRows
	}
	return id, nil
}

func (s *stubQuerier) InsertSubmission(_ context.Context, _ db.InsertSubmissionParams) (string, error) {
	return "", nil
}

// hashOf returns SHA-256(s) as a [32]byte.
func hashOf(s string) [32]byte {
	return sha256.Sum256([]byte(s))
}

// -------------------------------------------------------------------
// Helper: build handler under test
// -------------------------------------------------------------------

func newAuthMiddleware(q *stubQuerier) http.Handler {
	logger := zap.NewNop()
	sentinelHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := middleware.ContestantIDFromContext(r.Context())
		if !ok {
			http.Error(w, "no contestant_id in context", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Contestant-ID", id)
		w.WriteHeader(http.StatusOK)
	})
	return middleware.Auth(q, logger)(sentinelHandler)
}

// -------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------

// Test: missing Authorization header → 401.
func TestAuth_MissingHeader_Returns401(t *testing.T) {
	q := &stubQuerier{tokens: map[[32]byte]string{}}
	handler := newAuthMiddleware(q)

	req := httptest.NewRequest(http.MethodPost, "/v1/submissions", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// Test: Authorization header present but not "Bearer …" format → 401.
func TestAuth_MalformedBearerToken_Returns401(t *testing.T) {
	q := &stubQuerier{tokens: map[[32]byte]string{}}
	handler := newAuthMiddleware(q)

	for _, hdr := range []string{
		"Basic dXNlcjpwYXNz",    // Basic auth, not Bearer
		"Bearer",                 // no token after "Bearer"
		"Bearer ",                // only whitespace
		"Token abc123",           // wrong scheme
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/submissions", nil)
		req.Header.Set("Authorization", hdr)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("header %q: expected 401, got %d", hdr, rr.Code)
		}
	}
}

// Test: token that does not exist in the DB (simulates revoked / unknown) → 401.
func TestAuth_RevokedOrUnknownToken_Returns401(t *testing.T) {
	// Populate the stub with a known-good token that is NOT the one presented.
	knownToken := "known-valid-token"
	q := &stubQuerier{tokens: map[[32]byte]string{
		hashOf(knownToken): "contestant-uuid-1",
	}}
	handler := newAuthMiddleware(q)

	// Present a different, unknown token.
	req := httptest.NewRequest(http.MethodPost, "/v1/submissions", nil)
	req.Header.Set("Authorization", "Bearer unknown-token-xyz")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown token, got %d", rr.Code)
	}
}

// Test: valid token → next handler called and contestant_id injected in context.
func TestAuth_ValidToken_CallsNextWithContestantID(t *testing.T) {
	rawToken := "super-secret-contestant-token"
	expectedID := "contestant-uuid-42"
	q := &stubQuerier{tokens: map[[32]byte]string{
		hashOf(rawToken): expectedID,
	}}
	handler := newAuthMiddleware(q)

	req := httptest.NewRequest(http.MethodPost, "/v1/submissions", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Contestant-ID"); got != expectedID {
		t.Fatalf("expected contestant_id %q in context, got %q", expectedID, got)
	}
}

// Test: valid token with leading/trailing spaces still validates correctly
// (the "Bearer " prefix is stripped and token is trimmed).
func TestAuth_ValidTokenWithSpaces_CallsNext(t *testing.T) {
	rawToken := "trimmed-token"
	expectedID := "contestant-uuid-99"
	q := &stubQuerier{tokens: map[[32]byte]string{
		hashOf(rawToken): expectedID,
	}}
	handler := newAuthMiddleware(q)

	req := httptest.NewRequest(http.MethodPost, "/v1/submissions", nil)
	// Extra trailing space after the token — extractBearerToken trims it.
	req.Header.Set("Authorization", "Bearer "+rawToken+"  ")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// With trailing spaces the token won't match the hash exactly, so 401.
	// This verifies the implementation does NOT silently strip trailing spaces
	// from the token itself (only the "Bearer " prefix is stripped; then the
	// token is trimmed by strings.TrimSpace — which DOES strip trailing spaces).
	// Adjust expectation: the trimmed token IS used for hashing.
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (trimmed token still matches), got %d", rr.Code)
	}
}
