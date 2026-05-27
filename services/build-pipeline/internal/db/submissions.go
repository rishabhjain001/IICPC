// Package db provides database helpers for the Build Pipeline service.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UpdateSubmissionStatus sets the status column of a submission row and bumps
// updated_at to the current wall-clock time.
//
// Returns an error if no row matched submissionID or the UPDATE itself failed.
func UpdateSubmissionStatus(ctx context.Context, pool *pgxpool.Pool, submissionID, status string) error {
	tag, err := pool.Exec(ctx,
		`UPDATE submissions SET status = $1, updated_at = now() WHERE id = $2`,
		status, submissionID,
	)
	if err != nil {
		return fmt.Errorf("db: update submission %s status to %s: %w", submissionID, status, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("db: submission %s not found", submissionID)
	}
	return nil
}
