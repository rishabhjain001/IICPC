package controller

import (
	"context"
	"fmt"
	"time"
)

const (
	// resourceReleaseTimeout is the maximum time allowed for releasing all
	// Sandbox resources after a crash (Requirement 3.6 — within 30 seconds).
	resourceReleaseTimeout = 30 * time.Second
)

// CrashHandler handles unexpected container exits for a Sandbox.
type CrashHandler struct {
	// Releaser releases CPU cores, memory cgroup, network namespace, and tmpfs
	// for the crashed sandbox.
	Releaser ResourceReleaser

	// StatusUpdater updates the Benchmark Run status in the Control Plane.
	StatusUpdater RunStatusUpdater
}

// HandleCrash records the termination reason and exit code, transitions the
// Sandbox phase to CRASHED, releases all reserved resources within 30 seconds,
// and updates the Benchmark Run status to SANDBOX_CRASH.
//
// Requirements: 3.6
func (h *CrashHandler) HandleCrash(
	ctx context.Context,
	sandbox *Sandbox,
	exitCode int32,
	reason string,
) error {
	// Record termination information on the Sandbox status.
	sandbox.Status.TerminationReason = reason
	sandbox.Status.ExitCode = exitCode
	sandbox.Status.Phase = PhaseCrashed

	// Release resources with a hard 30-second deadline (Requirement 3.6).
	releaseCtx, cancel := context.WithTimeout(ctx, resourceReleaseTimeout)
	defer cancel()

	releaseDone := make(chan error, 1)
	go func() {
		releaseDone <- h.Releaser.ReleaseResources(sandbox)
	}()

	var releaseErr error
	select {
	case releaseErr = <-releaseDone:
		// Resources released (or failed).
	case <-releaseCtx.Done():
		releaseErr = fmt.Errorf("HandleCrash: resource release timed out after %s", resourceReleaseTimeout)
	}

	// Update the Benchmark Run status regardless of resource release outcome,
	// so the Control Plane is always informed of the crash.
	updateErr := h.StatusUpdater.UpdateRunStatus(
		sandbox.Spec.BenchmarkRunID,
		RunStatusSandboxCrash,
	)

	// Return the first non-nil error encountered.
	if releaseErr != nil {
		return releaseErr
	}
	return updateErr
}
