// Package controller implements the Kubernetes operator reconcile loop for
// Sandbox custom resources.
//
// Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 4.1, 4.2, 4.3, 4.4, 4.6
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/iicpc/dbhp/sandbox-controller/internal/api/v1alpha1"
	"github.com/iicpc/dbhp/sandbox-controller/internal/endpoint"
	"github.com/iicpc/dbhp/sandbox-controller/internal/network"
	"github.com/iicpc/dbhp/sandbox-controller/internal/podbuilder"
)

// Reconciler watches Sandbox resources and drives the sandbox lifecycle:
//
//  1. On create: call NetworkManager.CreateOverlay → build Pod spec → create Pod → set PENDING.
//  2. When Pod is Running: set phase to RUNNING and record the internal IP.
//     Then register endpoints in Redis within 5 s (Requirement 4.2) and probe
//     them with a 15 s TCP dial (Requirements 4.3, 4.4).
//     If all endpoints are UNAVAILABLE, mark run as NO_ENDPOINTS (Req 4.6).
//  3. On Pod creation error: set phase to FAILED.
//  4. On ErrOverlayCreationFailed: set phase to FAILED with terminationReason
//     "NETWORK_SETUP_FAILED" (Requirement 3.4).
type Reconciler struct {
	client           kubernetes.Interface
	dynamicClient    dynamic.Interface
	networkManager   network.NetworkManager
	endpointRegistry *endpoint.Registry
	log              *zap.Logger
}

// NewReconciler creates a Reconciler wired to the given Kubernetes clients.
func NewReconciler(
	client kubernetes.Interface,
	dynamicClient dynamic.Interface,
	nm network.NetworkManager,
	reg *endpoint.Registry,
	log *zap.Logger,
) *Reconciler {
	return &Reconciler{
		client:           client,
		dynamicClient:    dynamicClient,
		networkManager:   nm,
		endpointRegistry: reg,
		log:              log,
	}
}

// Reconcile processes a single Sandbox resource identified by namespace/name.
// It is called by the controller runtime whenever the Sandbox or the
// corresponding Pod changes.
//
// The function is idempotent: re-running it on an already-running sandbox is
// a no-op.
func (r *Reconciler) Reconcile(ctx context.Context, namespace, name string, spec v1alpha1.SandboxSpec, status v1alpha1.SandboxStatus) (v1alpha1.SandboxStatus, error) {
	log := r.log.With(
		zap.String("sandbox", name),
		zap.String("namespace", namespace),
		zap.String("submissionId", spec.SubmissionID),
		zap.String("benchmarkRunId", spec.BenchmarkRunID),
	)

	// --- Terminal or running states: nothing to do -----------------------
	switch status.Phase {
	case v1alpha1.SandboxPhaseRunning,
		v1alpha1.SandboxPhaseCompleted,
		v1alpha1.SandboxPhaseFailed,
		v1alpha1.SandboxPhaseCrashed,
		v1alpha1.SandboxPhaseUnhealthy:
		return status, nil
	}

	podName := "sandbox-" + spec.SubmissionID

	// --- Check whether the Pod already exists ----------------------------
	existingPod, err := r.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return status, fmt.Errorf("get pod %q: %w", podName, err)
	}

	if k8serrors.IsNotFound(err) {
		// Pod does not exist yet — create overlay network and then the Pod.
		return r.createSandbox(ctx, log, namespace, spec, status)
	}

	// Pod already exists — sync phase from Pod phase.
	return r.syncPhaseFromPod(ctx, log, spec, existingPod, status)
}

// createSandbox sets up the overlay network and creates the sandbox Pod.
func (r *Reconciler) createSandbox(
	ctx context.Context,
	log *zap.Logger,
	namespace string,
	spec v1alpha1.SandboxSpec,
	status v1alpha1.SandboxStatus,
) (v1alpha1.SandboxStatus, error) {
	// --- 1. Provision per-run overlay network (Requirement 3.4) ---------
	_, err := r.networkManager.CreateOverlay(ctx, spec.BenchmarkRunID)
	if err != nil {
		log.Error("failed to create overlay network",
			zap.String("benchmarkRunId", spec.BenchmarkRunID),
			zap.Error(err),
		)
		if errors.Is(err, network.ErrOverlayCreationFailed) {
			status.Phase = v1alpha1.SandboxPhaseFailed
			status.TerminationReason = "NETWORK_SETUP_FAILED"
			return status, nil // terminal state; caller writes status back
		}
		return status, fmt.Errorf("create overlay: %w", err)
	}

	// --- 2. Build the hardened Pod spec ----------------------------------
	pod := podbuilder.BuildPod(namespace, spec)

	// --- 3. Create the Pod ----------------------------------------------
	_, err = r.client.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		log.Error("failed to create sandbox pod",
			zap.String("podName", pod.Name),
			zap.Error(err),
		)
		// Best-effort: clean up the overlay we just created.
		_ = r.networkManager.DeleteOverlay(ctx, spec.BenchmarkRunID)

		status.Phase = v1alpha1.SandboxPhaseFailed
		status.TerminationReason = fmt.Sprintf("pod creation failed: %v", err)
		return status, nil
	}

	log.Info("sandbox pod created, transitioning to PENDING",
		zap.String("podName", pod.Name),
	)

	status.Phase = v1alpha1.SandboxPhasePending
	return status, nil
}

// syncPhaseFromPod advances the Sandbox phase based on the observed Pod phase.
// When the pod first transitions to Running, it registers endpoints in Redis
// (Requirement 4.2) and probes them via TCP (Requirements 4.3, 4.4).
func (r *Reconciler) syncPhaseFromPod(
	ctx context.Context,
	log *zap.Logger,
	spec v1alpha1.SandboxSpec,
	pod *corev1.Pod,
	status v1alpha1.SandboxStatus,
) (v1alpha1.SandboxStatus, error) {
	switch pod.Status.Phase {
	case corev1.PodRunning:
		if status.Phase != v1alpha1.SandboxPhaseRunning {
			log.Info("pod is Running, transitioning sandbox to RUNNING",
				zap.String("podName", pod.Name),
				zap.String("podIP", pod.Status.PodIP),
			)
			status.Phase = v1alpha1.SandboxPhaseRunning
			status.InternalIP = pod.Status.PodIP

			// Register and probe endpoints now that we have an IP.
			r.registerAndProbeEndpoints(ctx, log, spec, pod.Status.PodIP, &status)
		}

	case corev1.PodFailed:
		exitCode := int32(0)
		reason := pod.Status.Reason
		if len(pod.Status.ContainerStatuses) > 0 {
			cs := pod.Status.ContainerStatuses[0]
			if cs.State.Terminated != nil {
				exitCode = cs.State.Terminated.ExitCode
				if reason == "" {
					reason = cs.State.Terminated.Reason
				}
			}
		}
		log.Warn("pod failed",
			zap.String("podName", pod.Name),
			zap.String("reason", reason),
			zap.Int32("exitCode", exitCode),
		)
		status.Phase = v1alpha1.SandboxPhaseCrashed
		status.TerminationReason = reason
		status.ExitCode = exitCode

	case corev1.PodSucceeded:
		status.Phase = v1alpha1.SandboxPhaseCompleted

	case corev1.PodPending:
		// Still pending — no phase change needed.
		status.Phase = v1alpha1.SandboxPhasePending
	}

	return status, nil
}

// registerAndProbeEndpoints registers the sandbox endpoints in Redis within
// 5 seconds of RUNNING state (Requirement 4.2), then probes each endpoint
// over TCP with a 15-second timeout (Requirements 4.3, 4.4).  If all
// endpoints are UNAVAILABLE the sandbox status is updated to reflect
// NO_ENDPOINTS (Requirement 4.6).
func (r *Reconciler) registerAndProbeEndpoints(
	ctx context.Context,
	log *zap.Logger,
	spec v1alpha1.SandboxSpec,
	internalIP string,
	status *v1alpha1.SandboxStatus,
) {
	if r.endpointRegistry == nil {
		log.Warn("endpoint registry not configured; skipping endpoint registration")
		return
	}

	// Build the initial endpoint list from the protocols declared in the spec.
	endpoints := buildEndpoints(spec, internalIP)
	if len(endpoints) == 0 {
		log.Warn("no protocols declared in SandboxSpec; skipping endpoint registration",
			zap.String("benchmarkRunId", spec.BenchmarkRunID),
		)
		return
	}

	// Register within 5 seconds (Requirement 4.2).
	regCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := r.endpointRegistry.RegisterEndpoints(regCtx, spec.BenchmarkRunID, endpoints); err != nil {
		log.Error("failed to register endpoints",
			zap.String("benchmarkRunId", spec.BenchmarkRunID),
			zap.Error(err),
		)
		// Non-fatal at this point; probing will still attempt.
	}

	// Probe each endpoint with a 15-second TCP dial (Requirements 4.3, 4.4).
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	probed := endpoint.ProbeEndpoints(probeCtx, endpoints, func() {
		// All endpoints unavailable — Requirement 4.6.
		log.Warn("all sandbox endpoints UNAVAILABLE; marking run NO_ENDPOINTS",
			zap.String("benchmarkRunId", spec.BenchmarkRunID),
		)
		status.TerminationReason = "NO_ENDPOINTS"
	})

	// Log availability and persist updated statuses.
	for _, ep := range probed {
		log.Info("endpoint probe result",
			zap.String("benchmarkRunId", spec.BenchmarkRunID),
			zap.String("submissionId", spec.SubmissionID),
			zap.String("protocol", ep.Protocol),
			zap.Int32("port", ep.Port),
			zap.String("status", ep.Status),
			zap.String("internalIP", ep.InternalIP),
		)
		if err := r.endpointRegistry.UpdateEndpointStatus(ctx, spec.BenchmarkRunID, ep); err != nil {
			log.Error("failed to update endpoint status in registry",
				zap.String("protocol", ep.Protocol),
				zap.Error(err),
			)
		}
	}

	// Reflect endpoint statuses back into the Sandbox CRD status.
	epStatuses := make([]v1alpha1.EndpointStatus, 0, len(probed))
	for _, ep := range probed {
		epStatuses = append(epStatuses, v1alpha1.EndpointStatus{
			Protocol: ep.Protocol,
			Port:     ep.Port,
			Status:   ep.Status,
		})
	}
	status.Endpoints = epStatuses
}

// buildEndpoints constructs the initial (un-probed) endpoint list for a
// sandbox based on the protocols declared in its spec (Requirement 4.1).
func buildEndpoints(spec v1alpha1.SandboxSpec, internalIP string) []endpoint.EndpointInfo {
	portByProtocol := map[string]int32{
		"FIX":  endpoint.PortFIX,
		"REST": endpoint.PortREST,
		"WS":   endpoint.PortWebSocket,
	}

	out := make([]endpoint.EndpointInfo, 0, len(spec.Protocols))
	for _, proto := range spec.Protocols {
		port, ok := portByProtocol[proto]
		if !ok {
			continue
		}
		out = append(out, endpoint.EndpointInfo{
			Protocol:       proto,
			Port:           port,
			Status:         "UNAVAILABLE", // will be updated after probing
			InternalIP:     internalIP,
			SubmissionID:   spec.SubmissionID,
			BenchmarkRunID: spec.BenchmarkRunID,
		})
	}
	return out
}
