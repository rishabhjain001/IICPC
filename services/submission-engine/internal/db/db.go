// Package db provides a PostgreSQL connection pool and database queries for the
// Submission Engine. All queries are executed against the pgx/v5 pool; no ORM
// is used.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool is the pgx connection pool type re-exported for dependency injection.
type Pool = pgxpool.Pool

// NewPool opens and validates a pgx connection pool using the supplied DSN.
// The caller is responsible for calling pool.Close() on shutdown.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("db.NewPool: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db.NewPool: ping: %w", err)
	}
	return pool, nil
}

// InsertSubmissionParams holds the data required to insert a new row into the
// submissions table.
type InsertSubmissionParams struct {
	// ID is the UUID primary key (assigned by the caller before calling Insert).
	ID string
	// ContestantID is the UUID of the uploading contestant.
	ContestantID string
	// ArtifactType is "ELF_BINARY" or "SOURCE_ARCHIVE".
	ArtifactType string
	// ArtifactSize is the byte length of the artifact.
	ArtifactSize int64
	// ChecksumSHA256 is the raw 32-byte SHA-256 digest.
	ChecksumSHA256 []byte
	// Status is the initial status string, e.g. "UPLOADED".
	Status string
	// ArtifactURI is the registry path returned by the registry client, or "".
	ArtifactURI string
}

// Querier defines the database operations used by the Submission Engine. Using
// an interface enables unit-testing without a real Postgres instance.
type Querier interface {
	// LookupContestantByTokenHash returns the contestant_id (UUID as string)
	// whose SHA-256 token hash matches tokenHash. It returns ("", ErrNoRows) if
	// no active, non-revoked, non-expired token matches.
	LookupContestantByTokenHash(ctx context.Context, tokenHash []byte) (string, error)

	// InsertSubmission inserts a new row into the submissions table and returns
	// the newly-created submission ID (same as params.ID). The caller must
	// supply a pre-generated UUID as params.ID.
	InsertSubmission(ctx context.Context, params InsertSubmissionParams) (string, error)
}

// Queries implements Querier against a *pgxpool.Pool.
type Queries struct {
	pool *pgxpool.Pool
}

// NewQueries wraps pool in a Queries that satisfies the Querier interface.
func NewQueries(pool *pgxpool.Pool) *Queries {
	return &Queries{pool: pool}
}

// LookupContestantByTokenHash queries the contestant_tokens table for a
// non-revoked, non-expired token whose token_hash column equals tokenHash
// (SHA-256 digest as a 32-byte slice). On success it returns the matching
// contestant's UUID string. On no match it returns ("", pgx.ErrNoRows).
func (q *Queries) LookupContestantByTokenHash(ctx context.Context, tokenHash []byte) (string, error) {
	const query = `
		SELECT contestant_id::text
		FROM   contestant_tokens
		WHERE  token_hash   = $1
		  AND  revoked      = false
		  AND  (expires_at IS NULL OR expires_at > now())
		LIMIT 1`

	var contestantID string
	err := q.pool.QueryRow(ctx, query, tokenHash).Scan(&contestantID)
	if err != nil {
		return "", fmt.Errorf("db.LookupContestantByTokenHash: %w", err)
	}
	return contestantID, nil
}

// InsertSubmission inserts a new row into the submissions table with status
// "UPLOADED". It returns the inserted submission ID on success.
func (q *Queries) InsertSubmission(ctx context.Context, params InsertSubmissionParams) (string, error) {
	const query = `
		INSERT INTO submissions
			(id, contestant_id, artifact_type, artifact_size, checksum_sha256, status, artifact_uri)
		VALUES
			($1::uuid, $2::uuid, $3, $4, $5, $6, NULLIF($7, ''))
		RETURNING id::text`

	var id string
	err := q.pool.QueryRow(ctx, query,
		params.ID,
		params.ContestantID,
		params.ArtifactType,
		params.ArtifactSize,
		params.ChecksumSHA256,
		params.Status,
		params.ArtifactURI,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("db.InsertSubmission: %w", err)
	}
	return id, nil
}
