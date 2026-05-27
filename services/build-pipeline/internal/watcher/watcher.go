// Package watcher polls the database for UPLOADED submissions and dispatches
// hermetic Kubernetes build jobs for each one.
package watcher

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/build-pipeline/internal/db"
	"github.com/iicpc/dbhp/build-pipeline/internal/spawner"
	"github.com/iicpc/dbhp/shared-go/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	pollInterval = 5 * time.Second
	batchSize    = 10
)

// submissionRow is a lightweight projection of the columns needed for dispatch.
type submissionRow struct {
	id          string
	artifactURI string
}

// Watcher polls the submissions table for rows in the UPLOADED state and
// spawns a build Job for each one.
type Watcher struct {
	pool    *pgxpool.Pool
	spawner *spawner.JobSpawner
	logger  *zap.Logger
}

// New constructs a Watcher.
func New(pool *pgxpool.Pool, s *spawner.JobSpawner, logger *zap.Logger) *Watcher {
	return &Watcher{pool: pool, spawner: s, logger: logger}
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	w.logger.Info("watcher started", zap.Duration("poll_interval", pollInterval))

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("watcher stopped", zap.Error(ctx.Err()))
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// poll fetches up to batchSize UPLOADED submissions, marks them BUILDING
// (to prevent double-processing) and dispatches a build Job for each.
func (w *Watcher) poll(ctx context.Context) {
	rows, err := w.fetchUploaded(ctx)
	if err != nil {
		w.logger.Error("watcher: failed to fetch uploaded submissions", zap.Error(err))
		return
	}

	for _, row := range rows {
		// Transition to BUILDING immediately so the next poll cycle does not
		// pick up the same submission again, preventing duplicate Jobs.
		if err := w.markBuilding(ctx, row.id); err != nil {
			w.logger.Error("watcher: failed to mark submission BUILDING",
				zap.String("submission_id", row.id),
				zap.Error(err),
			)
			// Skip spawn — avoid double-processing ambiguity.
			continue
		}

		if err := w.spawner.SpawnBuildJob(ctx, row.id, row.artifactURI); err != nil {
			// SpawnBuildJob already set BUILD_INFRASTRUCTURE_ERROR on the DB
			// row; just log and continue to the next submission.
			w.logger.Error("watcher: SpawnBuildJob failed",
				zap.String("submission_id", row.id),
				zap.Error(err),
			)
		}
	}
}

// fetchUploaded returns up to batchSize submissions whose status is UPLOADED,
// ordered oldest-first so the queue drains in arrival order.
func (w *Watcher) fetchUploaded(ctx context.Context) ([]submissionRow, error) {
	pgRows, err := w.pool.Query(ctx,
		`SELECT id, artifact_uri
		   FROM submissions
		  WHERE status = $1
		  ORDER BY created_at
		  LIMIT $2`,
		types.SubmissionStatusUploaded,
		batchSize,
	)
	if err != nil {
		return nil, err
	}
	defer pgRows.Close()

	var results []submissionRow
	for pgRows.Next() {
		var r submissionRow
		if err := pgRows.Scan(&r.id, &r.artifactURI); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, pgRows.Err()
}

// markBuilding updates a submission's status to "BUILDING" directly.
// We use the raw string because the shared types package only defines
// BenchmarkRun BUILDING; for submissions the watcher owns this transition.
func (w *Watcher) markBuilding(ctx context.Context, submissionID string) error {
	return db.UpdateSubmissionStatus(ctx, w.pool, submissionID, "BUILDING")
}
