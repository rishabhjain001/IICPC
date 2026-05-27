// Package store provides PostgreSQL-backed persistence for Submission Engine
// domain objects.  It is distinct from the internal/db package (which owns the
// connection pool and low-level token lookup) and gives higher-level handlers a
// typed API over the submissions table.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by GetByID when no row with the given ID exists in
// the submissions table.
var ErrNotFound = errors.New("store: submission not found")

// Submission mirrors the columns of the submissions table that are needed by
// the Submission Engine's HTTP API.  Fields that are not yet populated (e.g.
// image_digest before a build completes) are represented as empty strings.
type Submission struct {
	// ID is the UUID primary key assigned at upload time.
	ID string
	// ContestantID is the UUID of the owning contestant.
	ContestantID string
	// Status is the current lifecycle status string (e.g. "UPLOADED",
	// "BUILD_FAILED", "BUILD_SUCCEEDED").
	Status string
	// BuildLogURI is the Artifact Registry path for the build log, or empty
	// string if no build log exists yet.
	BuildLogURI string
}

// SubmissionStore performs PostgreSQL queries against the submissions table.
type SubmissionStore struct {
	pool *pgxpool.Pool
}

// New returns a SubmissionStore backed by pool.
func New(pool *pgxpool.Pool) *SubmissionStore {
	return &SubmissionStore{pool: pool}
}

// GetByID fetches the submission whose primary key equals id.  It returns
// ErrNotFound when the row does not exist, or a wrapped pgx error for any
// other failure.
func (s *SubmissionStore) GetByID(ctx context.Context, id string) (Submission, error) {
	const query = `
		SELECT id::text,
		       contestant_id::text,
		       status,
		       COALESCE(build_log_uri, '') AS build_log_uri
		FROM   submissions
		WHERE  id = $1::uuid
		LIMIT  1`

	var sub Submission
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&sub.ID,
		&sub.ContestantID,
		&sub.Status,
		&sub.BuildLogURI,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Submission{}, ErrNotFound
		}
		return Submission{}, fmt.Errorf("store.GetByID: %w", err)
	}
	return sub, nil
}
