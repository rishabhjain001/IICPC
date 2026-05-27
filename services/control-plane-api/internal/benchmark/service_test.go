package benchmark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/iicpc/dbhp/control-plane-api/internal/benchmark"
	controlv1 "github.com/iicpc/dbhp/control-plane-api/generated/control/v1"
	"github.com/iicpc/dbhp/control-plane-api/internal/testutil"
	sharedtypes "github.com/iicpc/dbhp/shared-go/types"
)

// newService creates a BenchmarkService connected to the test database.
func newService(t *testing.T) *benchmark.BenchmarkService {
	t.Helper()
	pool := testutil.MustPool(t)
	return benchmark.NewBenchmarkService(pool, nil)
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

// seedSubmission inserts a minimal submission and returns its UUID.
func seedSubmission(t *testing.T, pool *pgxpool.Pool, contestantID string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO submissions (contestant_id, artifact_type, artifact_size, checksum_sha256)
		 VALUES ($1, 'ELF_BINARY', 1024, '\xdeadbeef')
		 RETURNING id`,
		contestantID,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seedSubmission: %v", err)
	}
	return id
}

// TestCreateRun_QueuedTransition verifies that CreateRun inserts a run with
// QUEUED status and writes an audit entry.
func TestCreateRun_QueuedTransition(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	cID := seedContestant(t, pool, "testuser_1", "test1@example.com")
	sID := seedSubmission(t, pool, cID)

	run, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
		ContestantID: cID,
		SubmissionID: sID,
		BotCount:     100,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.ID == "" {
		t.Fatal("expected non-empty run ID")
	}
	if run.Status != sharedtypes.BenchmarkRunStatusQueued {
		t.Errorf("expected status QUEUED, got %q", run.Status)
	}

	// Verify an audit row was written.
	var auditCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM benchmark_run_audit WHERE benchmark_run_id = $1`, run.ID,
	).Scan(&auditCount)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("expected 1 audit entry after CreateRun, got %d", auditCount)
	}
}

// TestTransitionState_ValidQueuedToBuilding verifies the QUEUED→BUILDING
// transition succeeds and records an audit entry.
func TestTransitionState_ValidQueuedToBuilding(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	cID := seedContestant(t, pool, "testuser_2", "test2@example.com")
	sID := seedSubmission(t, pool, cID)

	run, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
		ContestantID: cID,
		SubmissionID: sID,
		BotCount:     100,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := svc.TransitionState(ctx, run.ID, "scheduler", sharedtypes.BenchmarkRunStatusBuilding, ""); err != nil {
		t.Fatalf("TransitionState QUEUED→BUILDING: %v", err)
	}

	updated, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if updated.Status != sharedtypes.BenchmarkRunStatusBuilding {
		t.Errorf("expected BUILDING, got %q", updated.Status)
	}

	// Verify 2 audit entries: initial QUEUED + BUILDING.
	var auditCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM benchmark_run_audit WHERE benchmark_run_id = $1`, run.ID,
	).Scan(&auditCount)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if auditCount != 2 {
		t.Errorf("expected 2 audit entries, got %d", auditCount)
	}
}

// TestTransitionState_InvalidTransitionRejected verifies that an invalid
// state transition returns an error.
func TestTransitionState_InvalidTransitionRejected(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	cID := seedContestant(t, pool, "testuser_3", "test3@example.com")
	sID := seedSubmission(t, pool, cID)

	run, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
		ContestantID: cID,
		SubmissionID: sID,
		BotCount:     100,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// QUEUED → SCORING is invalid (skips multiple states).
	err = svc.TransitionState(ctx, run.ID, "scheduler", sharedtypes.BenchmarkRunStatusScoring, "")
	if err == nil {
		t.Fatal("expected error for invalid transition QUEUED→SCORING, got nil")
	}
}

// TestTransitionState_AuditEntryOnEveryTransition verifies that each
// successful transition writes exactly one audit row.
func TestTransitionState_AuditEntryOnEveryTransition(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	cID := seedContestant(t, pool, "testuser_4", "test4@example.com")
	sID := seedSubmission(t, pool, cID)

	run, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
		ContestantID: cID,
		SubmissionID: sID,
		BotCount:     100,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	transitions := []string{
		sharedtypes.BenchmarkRunStatusBuilding,
		sharedtypes.BenchmarkRunStatusDeploying,
		sharedtypes.BenchmarkRunStatusRunning,
	}

	for i, nextState := range transitions {
		if err := svc.TransitionState(ctx, run.ID, "scheduler", nextState, ""); err != nil {
			t.Fatalf("transition to %s: %v", nextState, err)
		}

		var auditCount int
		err = pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM benchmark_run_audit WHERE benchmark_run_id = $1`, run.ID,
		).Scan(&auditCount)
		if err != nil {
			t.Fatalf("audit query: %v", err)
		}
		// +1 for initial CreateRun audit entry, +i+1 for transitions so far.
		expected := 1 + (i + 1)
		if auditCount != expected {
			t.Errorf("after %d transitions: expected %d audit entries, got %d", i+1, expected, auditCount)
		}
	}
}

// TestCancelRun_SetsFailed verifies that CancelRun transitions a non-terminal
// run to FAILED.
func TestCancelRun_SetsFailed(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	cID := seedContestant(t, pool, "testuser_5", "test5@example.com")
	sID := seedSubmission(t, pool, cID)

	run, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
		ContestantID: cID,
		SubmissionID: sID,
		BotCount:     100,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := svc.CancelRun(ctx, run.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	updated, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if updated.Status != sharedtypes.BenchmarkRunStatusFailed {
		t.Errorf("expected FAILED after CancelRun, got %q", updated.Status)
	}
}

// TestCreateRun_ThirdRunAllowed verifies that creating a 3rd concurrent run
// (count = 2) succeeds.
func TestCreateRun_ThirdRunAllowed(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	cID := seedContestant(t, pool, "testuser_6", "test6@example.com")

	// Create 2 non-terminal runs.
	for i := 0; i < 2; i++ {
		sID := seedSubmission(t, pool, cID)
		_, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
			ContestantID: cID,
			SubmissionID: sID,
			BotCount:     100,
		})
		if err != nil {
			t.Fatalf("CreateRun %d: %v", i+1, err)
		}
	}

	// 3rd run should be allowed.
	sID := seedSubmission(t, pool, cID)
	run, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
		ContestantID: cID,
		SubmissionID: sID,
		BotCount:     100,
	})
	if err != nil {
		t.Fatalf("CreateRun (3rd): unexpected error: %v", err)
	}
	if run.ID == "" {
		t.Fatal("3rd run returned empty ID")
	}
}

// TestCreateRun_FourthRunRejectedWithResourceExhausted verifies that creating
// a 4th concurrent run returns a gRPC RESOURCE_EXHAUSTED error.
func TestCreateRun_FourthRunRejectedWithResourceExhausted(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	cID := seedContestant(t, pool, "testuser_7", "test7@example.com")

	// Create 3 non-terminal runs.
	for i := 0; i < 3; i++ {
		sID := seedSubmission(t, pool, cID)
		_, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
			ContestantID: cID,
			SubmissionID: sID,
			BotCount:     100,
		})
		if err != nil {
			t.Fatalf("CreateRun %d: %v", i+1, err)
		}
	}

	// 4th run must be rejected.
	sID := seedSubmission(t, pool, cID)
	_, err := svc.CreateRun(ctx, &controlv1.CreateRunRequest{
		ContestantID: cID,
		SubmissionID: sID,
		BotCount:     100,
	})
	if err == nil {
		t.Fatal("expected RESOURCE_EXHAUSTED error for 4th concurrent run, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		// If not a grpc status error, check if it wraps one.
		var grpcErr interface {
			GRPCStatus() *status.Status
		}
		if errors.As(err, &grpcErr) {
			st = grpcErr.GRPCStatus()
			ok = true
		}
	}
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v (type %T)", err, err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("expected RESOURCE_EXHAUSTED code, got %v", st.Code())
	}
}
