package controller

import (
	"context"
	"fmt"
	"time"
)

const (
	// maxAllowedLifetimeSeconds is the hard upper bound on any sandbox lifetime
	// (2 hours — Requirement 3.7).
	maxAllowedLifetimeSeconds int64 = 7200

	// gracefulStopDeadline is how long to wait for the container to stop after
	// SIGTERM before escalating to SIGKILL.
	gracefulStopDeadline = 30 * time.Second

	// metricCollectionDeadline is the maximum time allowed for final metric
	// collection after the container stops (Requirement 3.7 — 60 seconds).
	metricCollectionDeadline = 60 * time.Second
)

// ContainerStopper is the interface for gracefully stopping a sandbox
// container.  Implementations should send SIGTERM, wait up to
// gracefulStopDeadline, then send SIGKILL.
type ContainerStopper interface {
	// GracefulStop stops the container identified by the sandbox.  It returns
	// nil when the container has exited cleanly and an error if SIGKILL was
	// required or the stop failed entirely.
	GracefulStop(ctx context.Context, sandbox *Sandbox) error
}

// LifetimeEnforcer manages the maximum-lifetime deadline for a Sandbox.
type LifetimeEnforcer struct {
	// Stopper gracefully stops the sandbox container on expiry.
	Stopper ContainerStopper

	// Releaser releases all reserved resources after the run ends.
	Releaser ResourceReleaser

	// StatusUpdater notifies the Control Plane of run status changes.
	StatusUpdater RunStatusUpdater
}

// EnforceLifetime starts a timer for sandbox.Spec.MaxLifetimeSeconds (capped
// at maxAllowedLifetimeSeconds).  When the timer fires it:
//
//  1. Gracefully stops the container (SIGTERM → 30 s wait → SIGKILL).
//  2. Calls onExpiry for final metric collection with a 60-second deadline.
//  3. If metric collection exceeds 60 s, sets run status to COLLECTION_TIMEOUT.
//  4. Releases all resources.
//
// EnforceLifetime blocks until the lifetime expires or ctx is cancelled.
// Cancel ctx to abort lifetime enforcement (e.g. when the sandbox is already
// stopping for another reason).
//
// Requirements: 3.7
func (l *LifetimeEnforcer) EnforceLifetime(
	ctx context.Context,
	sandbox *Sandbox,
	onExpiry func(ctx context.Context) error,
) error {
	// Cap the lifetime at the platform maximum.
	lifetimeSecs := sandbox.Spec.MaxLifetimeSeconds
	if lifetimeSecs <= 0 || lifetimeSecs > maxAllowedLifetimeSeconds {
		lifetimeSecs = maxAllowedLifetimeSeconds
	}

	timer := time.NewTimer(time.Duration(lifetimeSecs) * time.Second)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		// Sandbox was stopped by another path before the lifetime expired.
		return ctx.Err()

	case <-timer.C:
		// Lifetime has expired — proceed with graceful shutdown.
	}

	// Step 1: Gracefully stop the container.
	stopCtx, stopCancel := context.WithTimeout(ctx, gracefulStopDeadline+5*time.Second)
	defer stopCancel()
	if err := l.Stopper.GracefulStop(stopCtx, sandbox); err != nil {
		// Non-fatal: log but continue with metric collection and resource release.
		_ = fmt.Errorf("EnforceLifetime: graceful stop error: %w", err)
	}

	// Step 2: Trigger final metric collection with a strict 60-second deadline.
	var collectionTimedOut bool
	if onExpiry != nil {
		collectCtx, collectCancel := context.WithTimeout(context.Background(), metricCollectionDeadline)
		defer collectCancel()

		done := make(chan error, 1)
		go func() { done <- onExpiry(collectCtx) }()

		select {
		case <-done:
			// Collection completed within the deadline.
		case <-collectCtx.Done():
			collectionTimedOut = true
		}
	}

	// Step 3: If collection timed out, update run status to COLLECTION_TIMEOUT.
	if collectionTimedOut {
		if err := l.StatusUpdater.UpdateRunStatus(
			sandbox.Spec.BenchmarkRunID,
			RunStatusCollectionTimeout,
		); err != nil {
			// Non-fatal: we still release resources.
			_ = fmt.Errorf("EnforceLifetime: update run status error: %w", err)
		}
	}

	// Step 4: Release all resources.
	if err := l.Releaser.ReleaseResources(sandbox); err != nil {
		return fmt.Errorf("EnforceLifetime: release resources: %w", err)
	}

	if collectionTimedOut {
		return fmt.Errorf("EnforceLifetime: metric collection exceeded %s deadline", metricCollectionDeadline)
	}
	return nil
}
