package contestant_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/iicpc/dbhp/control-plane-api/internal/contestant"
	"github.com/iicpc/dbhp/control-plane-api/internal/testutil"
)

// newService creates a ContestantService connected to the test database.
// The test is skipped when the DATABASE_URL environment variable is unset so
// that unit tests run without a live database in CI.
func newService(t *testing.T) *contestant.ContestantService {
	t.Helper()
	pool := testutil.MustPool(t)
	return contestant.NewContestantService(pool)
}

// seedContestant inserts a contestant and returns its UUID.
func seedContestant(t *testing.T, pool *pgxpool.Pool, handle, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO contestants (handle, email) VALUES ($1, $2) RETURNING id`,
		handle, email,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seedContestant: %v", err)
	}
	return id
}

// TestRegisterContestant_InsertAndReturnUUID verifies that RegisterContestant
// inserts a row in the contestants table and returns a non-empty UUID string.
func TestRegisterContestant_InsertAndReturnUUID(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	id, err := svc.RegisterContestant(ctx, "alice_42", "alice@example.com")
	if err != nil {
		t.Fatalf("RegisterContestant returned unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("RegisterContestant returned an empty UUID")
	}

	// Verify the row exists in the DB.
	var handle string
	err = svc.Pool.QueryRow(ctx,
		`SELECT handle FROM contestants WHERE id = $1`, id,
	).Scan(&handle)
	if err != nil {
		t.Fatalf("contestant row not found after RegisterContestant: %v", err)
	}
	if handle != "alice_42" {
		t.Errorf("expected handle %q, got %q", "alice_42", handle)
	}
}

// TestIssueToken_StoresSHA256AndReturnsRawToken verifies that IssueToken:
//  1. Returns a non-empty raw hex token.
//  2. Stores SHA-256(raw token) in contestant_tokens.token_hash, not the raw bytes.
func TestIssueToken_StoresSHA256AndReturnsRawToken(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	// Create a contestant first.
	contestantID, err := svc.RegisterContestant(ctx, "bob_99", "bob@example.com")
	if err != nil {
		t.Fatalf("RegisterContestant: %v", err)
	}

	rawToken, err := svc.IssueToken(ctx, contestantID)
	if err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}
	if rawToken == "" {
		t.Fatal("IssueToken returned an empty token")
	}
	// A hex-encoded 32-byte value is 64 characters.
	if len(rawToken) != 64 {
		t.Errorf("expected 64-char hex token, got length %d", len(rawToken))
	}

	// Compute the expected hash.
	expectedHash := sha256.Sum256([]byte(rawToken))

	// Verify the stored hash.
	var storedHash []byte
	err = svc.Pool.QueryRow(ctx,
		`SELECT token_hash FROM contestant_tokens WHERE contestant_id = $1`,
		contestantID,
	).Scan(&storedHash)
	if err != nil {
		t.Fatalf("token row not found after IssueToken: %v", err)
	}

	storedHex := hex.EncodeToString(storedHash)
	expectedHex := hex.EncodeToString(expectedHash[:])
	if storedHex != expectedHex {
		t.Errorf("stored token_hash mismatch\n  got  %s\n  want %s", storedHex, expectedHex)
	}

	// The raw token must NOT appear in the stored hash.
	if storedHex == rawToken {
		t.Error("token_hash stores the raw token instead of its SHA-256 digest")
	}
}

// TestRevokeToken_SetsRevokedTrue verifies that RevokeToken sets revoked=true
// for the identified token row.
func TestRevokeToken_SetsRevokedTrue(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	// Create contestant and issue a token.
	contestantID, err := svc.RegisterContestant(ctx, "carol_7", "carol@example.com")
	if err != nil {
		t.Fatalf("RegisterContestant: %v", err)
	}

	_, tokenID, err := svc.IssueTokenWithID(ctx, contestantID)
	if err != nil {
		t.Fatalf("IssueTokenWithID: %v", err)
	}

	// Revoke the token.
	if err := svc.RevokeToken(ctx, tokenID); err != nil {
		t.Fatalf("RevokeToken returned unexpected error: %v", err)
	}

	// Verify revoked=true in the DB.
	var revoked bool
	err = svc.Pool.QueryRow(ctx,
		`SELECT revoked FROM contestant_tokens WHERE id = $1`, tokenID,
	).Scan(&revoked)
	if err != nil {
		t.Fatalf("token row not found after RevokeToken: %v", err)
	}
	if !revoked {
		t.Error("expected revoked=true after RevokeToken, got false")
	}
}

// TestRevokeToken_NotFound verifies that revoking a non-existent token ID
// returns an error wrapping contestant.ErrNotFound.
func TestRevokeToken_NotFound(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	err := svc.RevokeToken(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected error for non-existent token, got nil")
	}
	if !errors.Is(err, contestant.ErrNotFound) {
		t.Errorf("expected ErrNotFound in error chain, got: %v", err)
	}
}
