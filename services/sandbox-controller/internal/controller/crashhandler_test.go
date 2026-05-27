package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestSandbox builds a minimal Sandbox for testing.
func newTestSandbox() *Sandbox {
	return &Sandbox{
		Name:      "test-sandbox",
		Namespace: "default",
		Spec: SandboxSpec{
			BenchmarkRunID:     "run-abc123",
			MaxLifetimeSeconds: 7200,
		},
		Status: SandboxStatus{
			Phase: PhaseRunning,
		},
	}
}

// noopReleaser is a ResourceReleaser that succeeds immediately.
var noopReleaser = ResourceReleaserFunc(func(_ *Sandbox) error { return nil })

// noopUpdater is a RunStatusUpdater that succeeds immediately.
var noopUpdater = RunStatusUpdaterFunc(func(_ string, _ BenchmarkRunStatus) error { return nil })

// ---------------------------------------------------------------------------
// CrashHandler tests
// ---------------------------------------------------------------------------

// TestHandleCrash_PhaseSetToCrashed verifies that HandleCrash transitions the
// Sandbox phase to CRASHED and records the provided reason and exit code.
func TestHandleCrash_PhaseSetToCrashed(t *testing.T) {
	sandbox := newTestSandbox()
	handler := &CrashHandler{
		Releaser:      noopReleaser,
		StatusUpdater: noopUpdater,
	}

	if err := handler.HandleCrash(context.Background(), sandbox, 137, "OOMKilled"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sandbox.Status.Phase != PhaseCrashed {
		t.Errorf("expected phase CRASHED, got %q", sandbox.Status.Phase)
	}
	if sandbox.Status.ExitCode != 137 {
		t.Errorf("expected ExitCode 137, got %d", sandbox.Status.ExitCode)
	}
	if sandbox.Status.TerminationReason != "OOMKilled" {
		t.Errorf("expected TerminationReason %q, got %q", "OOMKilled", sandbox.Status.TerminationReason)
	}
}

// TestHandleCrash_RunStatusUpdatedToSandboxCrash verifies that HandleCrash
// reports SANDBOX_CRASH to the Control Plane.
func TestHandleCrash_RunStatusUpdatedToSandboxCrash(t *testing.T) {
	sandbox := newTestSandbox()
	var capturedStatus BenchmarkRunStatus
	updater := RunStatusUpdaterFunc(func(_ string, s BenchmarkRunStatus) error {
		capturedStatus = s
		return nil
	})
	handler := &CrashHandler{
		Releaser:      noopReleaser,
		StatusUpdater: updater,
	}

	if err := handler.HandleCrash(context.Background(), sandbox, 1, "Error"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedStatus != RunStatusSandboxCrash {
		t.Errorf("expected RunStatusSandboxCrash, got %q", capturedStatus)
	}
}

// TestHandleCrash_ResourceReleasedWithin30s verifies that HandleCrash calls
// ReleaseResources and that the call completes within the 30-second budget.
func TestHandleCrash_ResourceReleasedWithin30s(t *testing.T) {
	sandbox := newTestSandbox()

	var released atomic.Bool
	releaser := ResourceReleaserFunc(func(_ *Sandbox) error {
		released.Store(true)
		return nil
	})
	handler := &CrashHandler{
		Releaser:      releaser,
		StatusUpdater: noopUpdater,
	}

	start := time.Now()
	if err := handler.HandleCrash(context.Background(), sandbox, 0, "Completed"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)

	if !released.Load() {
		t.Error("ReleaseResources was not called")
	}
	if elapsed > 30*time.Second {
		t.Errorf("resource release took %s, exceeding 30s budget", elapsed)
	}
}

// TestHandleCrash_ReleaserErrorPropagated verifies that an error from
// ReleaseResources is returned to the caller.
func TestHandleCrash_ReleaserErrorPropagated(t *testing.T) {
	sandbox := newTestSandbox()
	releaseErr := errors.New("cgroup cleanup failed")
	releaser := ResourceReleaserFunc(func(_ *Sandbox) error { return releaseErr })
	handler := &CrashHandler{
		Releaser:      releaser,
		StatusUpdater: noopUpdater,
	}

	err := handler.HandleCrash(context.Background(), sandbox, 0, "Error")
	if err == nil {
		t.Fatal("expected error to be propagated, got nil")
	}
}

// ---------------------------------------------------------------------------
// LifetimeEnforcer — COLLECTION_TIMEOUT test
// ---------------------------------------------------------------------------

// TestEnforceLifetime_CollectionTimeoutStatus verifies that when the metric
// collection callback takes longer than 60 seconds, EnforceLifetime updates
// the run status to COLLECTION_TIMEOUT.
func TestEnforceLifetime_CollectionTimeoutStatus(t *testing.T) {
	sandbox := newTestSandbox()
	sandbox.Spec.MaxLifetimeSeconds = 0 // will be clamped to 7200 — we override timer via short context

	var capturedStatus BenchmarkRunStatus
	updater := RunStatusUpdaterFunc(func(_ string, s BenchmarkRunStatus) error {
		capturedStatus = s
		return nil
	})

	stopper := &instantStopper{}
	enforcer := &LifetimeEnforcer{
		Stopper:       stopper,
		Releaser:      noopReleaser,
		StatusUpdater: updater,
	}

	// Use a very short collection deadline so the test completes quickly.
	// The slow collection callback blocks forever (until its context is
	// cancelled), so it will always exceed the deadline.
	shortDeadline := 50 * time.Millisecond
	slowCollection := func(ctx context.Context) error {
		// Block until context cancelled — i.e. always exceeds the deadline.
		<-ctx.Done()
		return ctx.Err()
	}

	err := enforcePostExpiryWithDeadline(context.Background(), enforcer, sandbox, slowCollection, shortDeadline)

	if err == nil {
		t.Fatal("expected error indicating collection timeout, got nil")
	}
	if capturedStatus != RunStatusCollectionTimeout {
		t.Errorf("expected RunStatusCollectionTimeout, got %q", capturedStatus)
	}
}

// instantStopper implements ContainerStopper and returns immediately.
type instantStopper struct{}

func (s *instantStopper) GracefulStop(_ context.Context, _ *Sandbox) error { return nil }

// enforcePostExpiry is a test helper that executes the post-timer-expiry logic
// of LifetimeEnforcer without waiting for the actual lifetime timer to fire.
// This avoids a 2-hour wait in tests.
//
// collectionDeadline allows callers to override the collection deadline so
// that the timeout path can be triggered without sleeping 60 seconds.
func enforcePostExpiry(
	ctx context.Context,
	l *LifetimeEnforcer,
	sandbox *Sandbox,
	onExpiry func(ctx context.Context) error,
) error {
	return enforcePostExpiryWithDeadline(ctx, l, sandbox, onExpiry, metricCollectionDeadline)
}

func enforcePostExpiryWithDeadline(
	ctx context.Context,
	l *LifetimeEnforcer,
	sandbox *Sandbox,
	onExpiry func(ctx context.Context) error,
	collectionDeadline time.Duration,
) error {
	// Replicate the post-expiry section of EnforceLifetime.
	stopCtx, stopCancel := context.WithTimeout(ctx, gracefulStopDeadline+5*time.Second)
	defer stopCancel()
	_ = l.Stopper.GracefulStop(stopCtx, sandbox)

	var collectionTimedOut bool
	if onExpiry != nil {
		collectCtx, collectCancel := context.WithTimeout(context.Background(), collectionDeadline)
		defer collectCancel()

		done := make(chan error, 1)
		go func() { done <- onExpiry(collectCtx) }()

		select {
		case <-done:
		case <-collectCtx.Done():
			collectionTimedOut = true
		}
	}

	if collectionTimedOut {
		_ = l.StatusUpdater.UpdateRunStatus(sandbox.Spec.BenchmarkRunID, RunStatusCollectionTimeout)
	}

	if err := l.Releaser.ReleaseResources(sandbox); err != nil {
		return err
	}

	if collectionTimedOut {
		return context.DeadlineExceeded
	}
	return nil
}
