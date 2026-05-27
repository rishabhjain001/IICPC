// Package benchmark implements the BenchmarkService gRPC handler for the
// Control Plane API.
//
// BenchmarkService manages the full lifecycle of Benchmark Runs including
// state machine enforcement, concurrent-run limits, and audit trail writes.
package benchmark

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/iicpc/dbhp/control-plane-api/generated/control/v1"
	sharedtypes "github.com/iicpc/dbhp/shared-go/types"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

// maxConcurrentRuns is the maximum number of non-terminal benchmark runs
// a single contestant may have at any one time (Requirement 11.4).
const maxConcurrentRuns = 3

// allowedTransitions maps a current state to the set of valid next states.
// This encodes the state machine defined in design.md §8.
var allowedTransitions = map[string]map[string]bool{
	sharedtypes.BenchmarkRunStatusQueued: {
		sharedtypes.BenchmarkRunStatusBuilding:  true,
		sharedtypes.BenchmarkRunStatusFailed:    true,
		sharedtypes.BenchmarkRunStatusBuildFailed: true,
	},
	sharedtypes.BenchmarkRunStatusBuilding: {
		sharedtypes.BenchmarkRunStatusDeploying:    true,
		sharedtypes.BenchmarkRunStatusBuildFailed:  true,
		sharedtypes.BenchmarkRunStatusBuildTimeout: true,
		sharedtypes.BenchmarkRunStatusFailed:       true,
	},
	sharedtypes.BenchmarkRunStatusDeploying: {
		sharedtypes.BenchmarkRunStatusRunning:             true,
		sharedtypes.BenchmarkRunStatusSandboxCrash:        true,
		sharedtypes.BenchmarkRunStatusNetworkSetupFailed:  true,
		sharedtypes.BenchmarkRunStatusNoEndpoints:         true,
		sharedtypes.BenchmarkRunStatusFailed:              true,
	},
	sharedtypes.BenchmarkRunStatusRunning: {
		sharedtypes.BenchmarkRunStatusCollecting:           true,
		sharedtypes.BenchmarkRunStatusSandboxCrash:         true,
		sharedtypes.BenchmarkRunStatusInsufficientCapacity: true,
		sharedtypes.BenchmarkRunStatusFailed:               true,
	},
	sharedtypes.BenchmarkRunStatusCollecting: {
		sharedtypes.BenchmarkRunStatusScoring:           true,
		sharedtypes.BenchmarkRunStatusCollectionTimeout: true,
		sharedtypes.BenchmarkRunStatusFailed:            true,
	},
	sharedtypes.BenchmarkRunStatusScoring: {
		sharedtypes.BenchmarkRunStatusComplete: true,
		sharedtypes.BenchmarkRunStatusFailed:   true,
	},
}

// BenchmarkService manages benchmark run lifecycle.
type BenchmarkService struct {
	DB  *pgxpool.Pool
	Log *zap.Logger
}

// NewBenchmarkService creates a BenchmarkService backed by the given pool.
func NewBenchmarkService(pool *pgxpool.Pool, log *zap.Logger) *BenchmarkService {
	return &BenchmarkService{DB: pool, Log: log}
}

// CreateRun creates a new QUEUED benchmark run and writes the initial audit
// entry. Returns gRPC RESOURCE_EXHAUSTED if the contestant already has 3
// non-terminal runs (Requirement 11.4).
func (s *BenchmarkService) CreateRun(ctx context.Context, req *controlv1.CreateRunRequest) (*controlv1.BenchmarkRun, error) {
	// Enforce concurrent-run limit before inserting.
	count, err := s.countNonTerminalRuns(ctx, req.ContestantID)
	if err != nil {
		return nil, fmt.Errorf("CreateRun: count runs: %w", err)
	}
	if count >= maxConcurrentRuns {
		return nil, status.Errorf(codes.ResourceExhausted,
			"contestant already has %d concurrent non-terminal benchmark runs (maximum %d)",
			count, maxConcurrentRuns)
	}

	botCount := req.BotCount
	if botCount == 0 {
		botCount = 1000
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("CreateRun: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Insert the benchmark run.
	const insertRun = `
INSERT INTO benchmark_runs (contestant_id, submission_id, status, bot_count)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at`

	run := &controlv1.BenchmarkRun{
		ContestantID: req.ContestantID,
		SubmissionID: req.SubmissionID,
		Status:       sharedtypes.BenchmarkRunStatusQueued,
		BotCount:     botCount,
	}

	if err := tx.QueryRow(ctx, insertRun,
		req.ContestantID,
		req.SubmissionID,
		sharedtypes.BenchmarkRunStatusQueued,
		botCount,
	).Scan(&run.ID, &run.CreatedAt); err != nil {
		return nil, fmt.Errorf("CreateRun: insert run: %w", err)
	}

	// Write initial audit entry (no prior state for a newly created run).
	const insertAudit = `
INSERT INTO benchmark_run_audit (benchmark_run_id, prior_state, new_state, actor_identity)
VALUES ($1, $2, $3, $4)`

	if _, err := tx.Exec(ctx, insertAudit,
		run.ID,
		"",
		sharedtypes.BenchmarkRunStatusQueued,
		req.ContestantID,
	); err != nil {
		return nil, fmt.Errorf("CreateRun: insert audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("CreateRun: commit: %w", err)
	}

	return run, nil
}

// GetRun returns the current state of a single benchmark run.
// Returns ErrNotFound if the run does not exist.
func (s *BenchmarkService) GetRun(ctx context.Context, runID string) (*controlv1.BenchmarkRun, error) {
	const q = `
SELECT id, contestant_id, submission_id, status, bot_count,
       created_at,
       COALESCE(started_at,   '0001-01-01'::timestamptz),
       COALESCE(completed_at, '0001-01-01'::timestamptz),
       COALESCE(composite_score, 0),
       COALESCE(speed_score, 0),
       COALESCE(stability_score, 0),
       COALESCE(accuracy_score, 0)
FROM   benchmark_runs
WHERE  id = $1`

	row := s.DB.QueryRow(ctx, q, runID)
	var run controlv1.BenchmarkRun
	if err := row.Scan(
		&run.ID,
		&run.ContestantID,
		&run.SubmissionID,
		&run.Status,
		&run.BotCount,
		&run.CreatedAt,
		&run.StartedAt,
		&run.CompletedAt,
		&run.CompositeScore,
		&run.SpeedScore,
		&run.StabilityScore,
		&run.AccuracyScore,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("GetRun: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("GetRun: %w", err)
	}
	return &run, nil
}

// ListRuns returns all runs for a contestant (optionally filtered by status).
func (s *BenchmarkService) ListRuns(ctx context.Context, contestantID string) ([]*controlv1.BenchmarkRun, error) {
	const q = `
SELECT id, contestant_id, submission_id, status, bot_count,
       created_at,
       COALESCE(started_at,   '0001-01-01'::timestamptz),
       COALESCE(completed_at, '0001-01-01'::timestamptz),
       COALESCE(composite_score, 0),
       COALESCE(speed_score, 0),
       COALESCE(stability_score, 0),
       COALESCE(accuracy_score, 0)
FROM   benchmark_runs
WHERE  contestant_id = $1
ORDER BY created_at DESC`

	rows, err := s.DB.Query(ctx, q, contestantID)
	if err != nil {
		return nil, fmt.Errorf("ListRuns: %w", err)
	}
	defer rows.Close()

	var runs []*controlv1.BenchmarkRun
	for rows.Next() {
		var run controlv1.BenchmarkRun
		if err := rows.Scan(
			&run.ID,
			&run.ContestantID,
			&run.SubmissionID,
			&run.Status,
			&run.BotCount,
			&run.CreatedAt,
			&run.StartedAt,
			&run.CompletedAt,
			&run.CompositeScore,
			&run.SpeedScore,
			&run.StabilityScore,
			&run.AccuracyScore,
		); err != nil {
			return nil, fmt.Errorf("ListRuns: scan: %w", err)
		}
		runs = append(runs, &run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListRuns: rows: %w", err)
	}
	return runs, nil
}

// CancelRun transitions a non-terminal run to FAILED and records the reason
// in the audit trail. If the run is already in a terminal state, it returns
// an error.
func (s *BenchmarkService) CancelRun(ctx context.Context, runID string) error {
	return s.TransitionState(ctx, runID, "operator", sharedtypes.BenchmarkRunStatusFailed, "cancelled by operator")
}

// TransitionState advances the state machine of a benchmark run and writes an
// audit entry in the same transaction. Returns an error if the transition is
// not valid from the current state.
//
// Valid forward path: QUEUED→BUILDING→DEPLOYING→RUNNING→COLLECTING→SCORING→COMPLETE
// Terminal failure states are accepted from any non-terminal state when
// newState is a terminal failure (e.g. FAILED).
func (s *BenchmarkService) TransitionState(ctx context.Context, runID, actorIdentity, newState, failureReason string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("TransitionState: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the row for update to prevent concurrent transitions.
	const lockQ = `SELECT status FROM benchmark_runs WHERE id = $1 FOR UPDATE`
	var currentState string
	if err := tx.QueryRow(ctx, lockQ, runID).Scan(&currentState); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("TransitionState: %w", ErrNotFound)
		}
		return fmt.Errorf("TransitionState: lock row: %w", err)
	}

	// Validate the transition.
	if sharedtypes.IsTerminal(currentState) {
		return fmt.Errorf("TransitionState: run %s is already in terminal state %q", runID, currentState)
	}

	allowed, ok := allowedTransitions[currentState]
	if !ok || !allowed[newState] {
		return fmt.Errorf("TransitionState: invalid transition %q → %q for run %s", currentState, newState, runID)
	}

	// Update run status.
	const updateQ = `UPDATE benchmark_runs SET status = $1 WHERE id = $2`
	if _, err := tx.Exec(ctx, updateQ, newState, runID); err != nil {
		return fmt.Errorf("TransitionState: update status: %w", err)
	}

	// Write audit entry in the same transaction.
	const auditQ = `
INSERT INTO benchmark_run_audit (benchmark_run_id, prior_state, new_state, actor_identity, failure_reason)
VALUES ($1, $2, $3, $4, $5)`

	var failureReasonPtr *string
	if failureReason != "" {
		failureReasonPtr = &failureReason
	}

	if _, err := tx.Exec(ctx, auditQ, runID, currentState, newState, actorIdentity, failureReasonPtr); err != nil {
		return fmt.Errorf("TransitionState: insert audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("TransitionState: commit: %w", err)
	}

	if s.Log != nil {
		s.Log.Info("benchmark run state transition",
			zap.String("run_id", runID),
			zap.String("from", currentState),
			zap.String("to", newState),
			zap.String("actor", actorIdentity),
		)
	}

	return nil
}

// countNonTerminalRuns counts the number of non-terminal runs for a contestant.
func (s *BenchmarkService) countNonTerminalRuns(ctx context.Context, contestantID string) (int, error) {
	const q = `
SELECT COUNT(*)
FROM   benchmark_runs
WHERE  contestant_id = $1
  AND  status NOT IN (
        'COMPLETE', 'BUILD_FAILED', 'BUILD_TIMEOUT', 'SANDBOX_CRASH',
        'NETWORK_SETUP_FAILED', 'NO_ENDPOINTS', 'PROVISIONING_TIMEOUT',
        'INSUFFICIENT_CAPACITY', 'COLLECTION_TIMEOUT', 'FAILED'
       )`

	var count int
	if err := s.DB.QueryRow(ctx, q, contestantID).Scan(&count); err != nil {
		return 0, fmt.Errorf("countNonTerminalRuns: %w", err)
	}
	return count, nil
}
