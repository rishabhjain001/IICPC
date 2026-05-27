package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// WatchPod streams pod phase-change events for the named pod into the events
// channel.  The function returns only after ctx is cancelled, the watch is
// closed by the API server, or a terminal pod phase is observed.
//
// On unexpected container exit (i.e. ContainerStatuses shows a Terminated
// state that was NOT a requested graceful stop), a PodEvent with Phase
// "CRASHED" is sent.
//
// Requirements: 3.6 — record termination reason and exit code on unexpected
// container exit.
func WatchPod(
	ctx context.Context,
	client kubernetes.Interface,
	podName, namespace string,
	events chan<- PodEvent,
) error {
	watcher, err := client.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", podName),
		Watch:         true,
	})
	if err != nil {
		return fmt.Errorf("WatchPod: start watch: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-watcher.ResultChan():
			if !ok {
				// Channel closed — watch connection dropped or API server
				// terminated the stream.
				return fmt.Errorf("WatchPod: watch channel closed unexpectedly")
			}

			switch ev.Type { //nolint:exhaustive
			case watch.Error:
				return fmt.Errorf("WatchPod: received error event")

			case watch.Modified, watch.Added:
				pod, ok := ev.Object.(*corev1.Pod)
				if !ok {
					continue
				}
				event := podEventFromPod(pod)
				if event == nil {
					continue
				}
				select {
				case events <- *event:
				case <-ctx.Done():
					return ctx.Err()
				}
				// Stop watching once a terminal phase is reached.
				if isTerminalPhase(event.Phase) {
					return nil
				}
			}
		}
	}
}

// podEventFromPod converts a corev1.Pod into a PodEvent.  Returns nil if
// there is no meaningful phase transition to report.
func podEventFromPod(pod *corev1.Pod) *PodEvent {
	phase := string(pod.Status.Phase)
	ts := time.Now()
	if !pod.Status.StartTime.IsZero() {
		ts = pod.Status.StartTime.Time
	}

	// Check container statuses for unexpected termination.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated == nil {
			continue
		}
		t := cs.State.Terminated

		// A graceful stop requested by the operator uses a specific exit code
		// (143 = SIGTERM; 137 = SIGKILL) AND the reason "Completed" or "OOMKilled".
		// Anything else that is not "Completed" is treated as a crash.
		if t.Reason != "Completed" {
			return &PodEvent{
				Phase:     "CRASHED",
				Reason:    t.Reason,
				ExitCode:  t.ExitCode,
				Timestamp: ts,
			}
		}
	}

	return &PodEvent{
		Phase:     phase,
		Reason:    "",
		ExitCode:  0,
		Timestamp: ts,
	}
}

// isTerminalPhase returns true for pod phases from which there is no
// recovery (Succeeded / Failed) or our synthetic CRASHED phase.
func isTerminalPhase(phase string) bool {
	switch phase {
	case "CRASHED", string(corev1.PodSucceeded), string(corev1.PodFailed):
		return true
	}
	return false
}
