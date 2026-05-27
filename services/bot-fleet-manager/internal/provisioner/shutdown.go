package provisioner

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// gracefulShutdownTimeout is the time allowed for graceful SIGTERM before
	// force-kill (Requirement 5.6).
	gracefulShutdownTimeout = 30 * time.Second

	// gracefulGracePeriod is the gracePeriodSeconds used for the initial delete.
	gracefulGracePeriod int64 = 30

	// forceGracePeriod is the gracePeriodSeconds used for force-kill.
	forceGracePeriod int64 = 0
)

// ShutdownFleet gracefully terminates all bots within 30 seconds.
// On timeout it forcibly kills any remaining pods and releases resources.
//
// Steps:
//  1. Delete all bot pods with gracePeriodSeconds=30 (sends SIGTERM).
//  2. Wait up to 30 seconds for pods to disappear.
//  3. Force-delete any pods still present (gracePeriodSeconds=0).
func ShutdownFleet(ctx context.Context, client kubernetes.Interface, runID string, logger *zap.Logger) error {
	propagation := metav1.DeletePropagationBackground
	deleteGraceful := metav1.DeleteOptions{
		GracePeriodSeconds: &[]int64{gracefulGracePeriod}[0],
		PropagationPolicy:  &propagation,
	}

	// List pods belonging to this run.
	podList, err := client.CoreV1().Pods(botNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("benchmark-run-id=%s", runID),
	})
	if err != nil {
		return fmt.Errorf("list pods for run %s: %w", runID, err)
	}

	if len(podList.Items) == 0 {
		logger.Info("no pods to shut down", zap.String("run_id", runID))
		return nil
	}

	logger.Info("graceful shutdown: sending SIGTERM to all bot pods",
		zap.String("run_id", runID),
		zap.Int("pod_count", len(podList.Items)),
	)

	// Send SIGTERM (delete with grace period).
	for _, pod := range podList.Items {
		if err := client.CoreV1().Pods(botNamespace).Delete(ctx, pod.Name, deleteGraceful); err != nil {
			logger.Warn("failed to gracefully delete pod",
				zap.String("pod", pod.Name),
				zap.Error(err),
			)
		}
	}

	// Wait up to gracefulShutdownTimeout for pods to terminate.
	deadline := time.Now().Add(gracefulShutdownTimeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			remaining, err := listRemainingPods(ctx, client, runID)
			if err != nil {
				logger.Warn("error listing remaining pods", zap.Error(err))
				continue
			}
			if len(remaining) == 0 {
				logger.Info("graceful shutdown complete", zap.String("run_id", runID))
				return nil
			}
			logger.Info("waiting for pods to terminate",
				zap.String("run_id", runID),
				zap.Int("remaining", len(remaining)),
			)
		}
	}

	// Force-kill any remaining pods.
	remaining, err := listRemainingPods(ctx, client, runID)
	if err != nil {
		return fmt.Errorf("list remaining pods: %w", err)
	}

	if len(remaining) == 0 {
		return nil
	}

	logger.Warn("graceful shutdown timed out, force-killing remaining pods",
		zap.String("run_id", runID),
		zap.Int("remaining", len(remaining)),
	)

	forceDeleteOpts := metav1.DeleteOptions{
		GracePeriodSeconds: &[]int64{forceGracePeriod}[0],
		PropagationPolicy:  &propagation,
	}

	var lastErr error
	for _, podName := range remaining {
		if err := client.CoreV1().Pods(botNamespace).Delete(ctx, podName, forceDeleteOpts); err != nil {
			logger.Error("failed to force-delete pod",
				zap.String("pod", podName),
				zap.Error(err),
			)
			lastErr = err
		}
	}
	return lastErr
}

// listRemainingPods returns the names of pods still present for the given run.
func listRemainingPods(ctx context.Context, client kubernetes.Interface, runID string) ([]string, error) {
	podList, err := client.CoreV1().Pods(botNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("benchmark-run-id=%s", runID),
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(podList.Items))
	for _, pod := range podList.Items {
		names = append(names, pod.Name)
	}
	return names, nil
}
