// Package contestant implements the ContestantService gRPC handler for the
// Control Plane API.
//
// ContestantService manages contestant registration and API bearer-token
// lifecycle. Raw tokens are never persisted; only SHA-256(token) is stored.
package contestant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when the requested resource does not exist.
var ErrNotFound = errors.New("not found")

// ContestantService handles contestant registration and token management.
type ContestantService struct {
	Pool *pgxpool.Pool
}

// NewContestantService creates a ContestantService backed by the given
// PostgreSQL connection pool.
func NewContestantService(pool *pgxpool.Pool) *ContestantService {
	return &ContestantService{Pool: pool}
}

// RegisterContestant inserts a new contestant row and returns the generated
// contestant UUID.
//
// Returns an error if the handle or email already exist (UNIQUE constraint
// violation) or on any other database error.
func (s *ContestantService) RegisterContestant(ctx context.Context, handle, email string) (string, error) {
	const q = `
INSERT INTO contestants (handle, email)
VALUES ($1, $2)
RETURNING id`

	var id string
	if err := s.Pool.QueryRow(ctx, q, handle, email).Scan(&id); err != nil {
		return "", fmt.Errorf("RegisterContestant: %w", err)
	}
	return id, nil
}

// IssueToken generates a cryptographically random 32-byte token, stores
// SHA-256(token) in the contestant_tokens table, and returns the raw hex token.
//
// The raw token value is only available at issuance. The caller is responsible
// for delivering it to the contestant securely.
func (s *ContestantService) IssueToken(ctx context.Context, contestantID string) (string, error) {
	// Generate 32 random bytes.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("IssueToken: generate random bytes: %w", err)
	}

	// The raw token is hex-encoded (64 chars).
	rawToken := hex.EncodeToString(raw)

	// Store SHA-256(rawToken) — not SHA-256(raw bytes) — so that verification
	// by the Submission Engine can also operate on the hex string.
	hash := sha256.Sum256([]byte(rawToken))

	const q = `
INSERT INTO contestant_tokens (contestant_id, token_hash)
VALUES ($1, $2)
RETURNING id`

	var tokenID string
	if err := s.Pool.QueryRow(ctx, q, contestantID, hash[:]).Scan(&tokenID); err != nil {
		return "", fmt.Errorf("IssueToken: insert token: %w", err)
	}

	return rawToken, nil
}

// IssueTokenWithID is like IssueToken but additionally returns the token
// record UUID (used by the gRPC handler to populate TokenResponse.TokenID).
func (s *ContestantService) IssueTokenWithID(ctx context.Context, contestantID string) (rawToken, tokenID string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("IssueTokenWithID: generate random bytes: %w", err)
	}
	rawToken = hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(rawToken))

	const q = `
INSERT INTO contestant_tokens (contestant_id, token_hash)
VALUES ($1, $2)
RETURNING id`

	if err := s.Pool.QueryRow(ctx, q, contestantID, hash[:]).Scan(&tokenID); err != nil {
		return "", "", fmt.Errorf("IssueTokenWithID: insert token: %w", err)
	}
	return rawToken, tokenID, nil
}

// RevokeToken sets revoked=true for the token row identified by tokenID (UUID).
//
// tokenID is the UUID primary key of the contestant_tokens row, not the hash.
// Returns ErrNotFound if no such token record exists.
func (s *ContestantService) RevokeToken(ctx context.Context, tokenID string) error {
	const q = `
UPDATE contestant_tokens
SET    revoked = true
WHERE  id = $1`

	tag, err := s.Pool.Exec(ctx, q, tokenID)
	if err != nil {
		return fmt.Errorf("RevokeToken: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("RevokeToken: %w", ErrNotFound)
	}
	return nil
}
