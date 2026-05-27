// Package provisioner manages the lifecycle of a bot fleet for a single
// Benchmark Run, including deploying bot Kubernetes pods and enforcing
// provisioning timeouts.
package provisioner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	botfleetv1 "github.com/iicpc/dbhp/bot-fleet-manager/generated/botfleet/v1"
	"github.com/iicpc/dbhp/bot-fleet-manager/internal/scheduler"
	"github.com/iicpc/dbhp/bot-fleet-manager/internal/store"
)

const (
	// botNamespace is the Kubernetes namespace where bot pods are deployed.
	botNamespace = "bot-fleet"

	// botImage is the container image used for synthetic trading bots.
	botImage = "iicpc/dbhp-bot-worker:latest"

	// provisioning timeouts per Requirement 5.3.
	timeoutSmallFleet = 60 * time.Second  // fleet size <= 1000
	timeoutLargeFleet = 180 * time.Second // fleet size <= 10000

	// podReadyPollInterval is how often we check pod readiness.
	podReadyPollInterval = 2 * time.Second
)

// ErrProvisioningTimeout is returned when not all pods reach READY within the
// timeout window.
var ErrProvisioningTimeout = errors.New("fleet provisioning timed out")

// Provisioner manages the lifecycle of a bot fleet for a single Benchmark Run.
type Provisioner struct {
	K8sClient kubernetes.Interface
	Logger    *zap.Logger
	Store     *store.FleetStore
}

// ProvisionFleet deploys bot pods and waits for all to reach READY.
//
// Timeout rules (Requirement 5.3):
//   - fleetSize <= 1000 → 60 s
//   - fleetSize <= 10000 → 180 s
//
// On timeout: sets run status to PROVISIONING_TIMEOUT and releases all pods.
func (p *Provisioner) ProvisionFleet(
	ctx context.Context,
	runID string,
	assignments []scheduler.BotAssignment,
	endpoints []botfleetv1.EndpointRef,
) error {
	fleetSize := len(assignments)

	// Determine provisioning timeout.
	timeout := provisioningTimeout(fleetSize)
	p.Logger.Info("provisioning fleet",
		zap.String("run_id", runID),
		zap.Int("fleet_size", fleetSize),
		zap.Duration("timeout", timeout),
	)

	// Initialise fleet record.
	p.Store.Create(runID, fleetSize)
	p.Store.UpdatePhase(runID, store.PhaseStarting)

	// Create a timeout context derived from the caller's context.
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Deploy all pods.
	for _, a := range assignments {
		podName := fmt.Sprintf("bot-%s-%d", runID[:8], a.BotIndex)
		pod := buildBotPod(podName, runID, a, endpoints)
		if _, err := p.K8sClient.CoreV1().Pods(botNamespace).Create(timeoutCtx, pod, metav1.CreateOptions{}); err != nil {
			p.Logger.Error("failed to create bot pod",
				zap.String("pod", podName),
				zap.Error(err),
			)
			// Best-effort cleanup and propagate error.
			_ = p.ReleaseFleet(ctx, runID)
			p.Store.UpdatePhase(runID, store.PhaseFailed)
			return fmt.Errorf("create pod %s: %w", podName, err)
		}
		p.Store.AddPod(runID, podName)
	}

	// Wait for all pods to reach READY.
	if err := p.waitForReady(timeoutCtx, runID, fleetSize); err != nil {
		if errors.Is(err, ErrProvisioningTimeout) || errors.Is(err, context.DeadlineExceeded) {
			p.Logger.Warn("fleet provisioning timed out, releasing pods",
				zap.String("run_id", runID),
			)
			p.Store.UpdatePhase(runID, store.PhaseTimeout)
			// Release partially-provisioned pods (use background context since
			// timeoutCtx has already expired).
			_ = p.ReleaseFleet(context.Background(), runID)
			return ErrProvisioningTimeout
		}
		return err
	}

	p.Store.UpdatePhase(runID, store.PhaseReady)
	p.Logger.Info("fleet provisioned successfully",
		zap.String("run_id", runID),
		zap.Int("fleet_size", fleetSize),
	)
	return nil
}

// ReleaseFleet deletes all bot pods for the given run.
func (p *Provisioner) ReleaseFleet(ctx context.Context, runID string) error {
	record := p.Store.Get(runID)
	if record == nil {
		return nil
	}

	propagationPolicy := metav1.DeletePropagationBackground
	deleteOpts := metav1.DeleteOptions{PropagationPolicy: &propagationPolicy}

	var lastErr error
	for _, podName := range record.PodNames {
		if err := p.K8sClient.CoreV1().Pods(botNamespace).Delete(ctx, podName, deleteOpts); err != nil {
			p.Logger.Warn("failed to delete bot pod",
				zap.String("pod", podName),
				zap.Error(err),
			)
			lastErr = err
		}
	}

	p.Store.UpdatePhase(runID, store.PhaseTerminated)
	return lastErr
}

// waitForReady polls until all pods for runID are Ready or the context expires.
func (p *Provisioner) waitForReady(ctx context.Context, runID string, fleetSize int) error {
	ticker := time.NewTicker(podReadyPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ErrProvisioningTimeout
		case <-ticker.C:
			ready, err := p.countReadyPods(ctx, runID)
			if err != nil {
				p.Logger.Warn("error counting ready pods", zap.Error(err))
				continue
			}
			p.Logger.Info("fleet readiness check",
				zap.String("run_id", runID),
				zap.Int("ready", ready),
				zap.Int("total", fleetSize),
			)
			if ready >= fleetSize {
				return nil
			}
		}
	}
}

// countReadyPods lists pods for the run and counts those with READY condition.
func (p *Provisioner) countReadyPods(ctx context.Context, runID string) (int, error) {
	podList, err := p.K8sClient.CoreV1().Pods(botNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("benchmark-run-id=%s", runID),
	})
	if err != nil {
		return 0, err
	}

	ready := 0
	for i := range podList.Items {
		if isPodReady(&podList.Items[i]) {
			ready++
		}
	}
	return ready, nil
}

// isPodReady returns true when the pod's Ready condition is True.
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// buildBotPod constructs the Kubernetes Pod spec for a single bot.
func buildBotPod(
	podName, runID string,
	a scheduler.BotAssignment,
	endpoints []botfleetv1.EndpointRef,
) *corev1.Pod {
	endpointIDs := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		endpointIDs = append(endpointIDs, ep.ID)
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: botNamespace,
			Labels: map[string]string{
				"app":              "bot-worker",
				"benchmark-run-id": runID,
				"bot-scenario":     string(a.Scenario),
			},
		},
		Spec: corev1.PodSpec{
			NodeName:      a.NodeName,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  "bot",
					Image: botImage,
					Env: []corev1.EnvVar{
						{Name: "BOT_SCENARIO", Value: string(a.Scenario)},
						{Name: "BENCHMARK_RUN_ID", Value: runID},
						{Name: "BOT_INDEX", Value: fmt.Sprintf("%d", a.BotIndex)},
					},
				},
			},
		},
	}
}

// provisioningTimeout returns the timeout for the given fleet size per Req 5.3.
func provisioningTimeout(fleetSize int) time.Duration {
	if fleetSize <= 1000 {
		return timeoutSmallFleet
	}
	return timeoutLargeFleet
}
