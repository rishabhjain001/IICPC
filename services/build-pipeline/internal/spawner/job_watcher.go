// Package spawner — job_watcher.go watches a Kubernetes Job until it reaches
// an active or terminal state, or the caller-supplied timeout elapses.
//
// Requirements: 2.1 — if the build container fails to start within 60 seconds
// the submission must be marked BUILD_INFRASTRUCTURE_ERROR.
package spawner

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"

	"github.com/iicpc/dbhp/shared-go/types"
)

// JobResult carries the outcome observed by WatchBuildJob.
type JobResult struct {
	// Started is true once the Job has at least one Active pod (i.e. the build
	// container actually started running).
	Started bool

	// Succeeded is true when the Job's Complete condition is True.
	Succeeded bool

	// Failed is true when the Job's Failed condition is True, or when the Job
	// was not scheduled within the timeout window.
	Failed bool

	// Reason is a short machine-readable string explaining why the job failed,
	// e.g. types.SubmissionStatusBuildInfrastructureError.
	Reason string
}

// WatchBuildJob watches the Kubernetes Job identified by jobName/namespace and
// blocks until one of the following conditions is met:
//
//  1. The Job reaches Active >= 1 → JobResult{Started: true}
//  2. The Job completes successfully → JobResult{Started: true, Succeeded: true}
//  3. The Job fails → JobResult{Started: true, Failed: true}
//  4. timeoutSecs elapses without the Job becoming active →
//     JobResult{Failed: true, Reason: BUILD_INFRASTRUCTURE_ERROR}
//
// The returned error is non-nil only when the watch itself cannot be
// established (e.g. the API server is unreachable).
func WatchBuildJob(
	ctx context.Context,
	client kubernetes.Interface,
	jobName, namespace string,
	timeoutSecs int64,
) (JobResult, error) {
	// Start a watch scoped to this single Job.
	w, err := client.BatchV1().Jobs(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector:  fmt.Sprintf("metadata.name=%s", jobName),
		TimeoutSeconds: &timeoutSecs,
	})
	if err != nil {
		return JobResult{
			Failed: true,
			Reason: types.SubmissionStatusBuildInfrastructureError,
		}, fmt.Errorf("spawner: watch job %s: %w", jobName, err)
	}
	defer w.Stop()

	deadline := time.Duration(timeoutSecs) * time.Second
	timer := time.NewTimer(deadline)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return JobResult{
				Failed: true,
				Reason: types.SubmissionStatusBuildInfrastructureError,
			}, ctx.Err()

		case <-timer.C:
			// Timeout elapsed; the job never became active.
			return JobResult{
				Failed: true,
				Reason: types.SubmissionStatusBuildInfrastructureError,
			}, nil

		case event, ok := <-w.ResultChan():
			if !ok {
				// Watch channel closed — treat as a timeout.
				return JobResult{
					Failed: true,
					Reason: types.SubmissionStatusBuildInfrastructureError,
				}, nil
			}

			if event.Type == watch.Error {
				return JobResult{
					Failed: true,
					Reason: types.SubmissionStatusBuildInfrastructureError,
				}, fmt.Errorf("spawner: watch error event for job %s", jobName)
			}

			job, ok := event.Object.(*batchv1.Job)
			if !ok {
				continue
			}

			result, done := evaluateJobStatus(job)
			if done {
				return result, nil
			}
		}
	}
}

// evaluateJobStatus inspects a Job object and returns (result, true) when a
// terminal or started state is reached, or (zero, false) to keep watching.
func evaluateJobStatus(job *batchv1.Job) (JobResult, bool) {
	// Job has active pods → it has started.
	if job.Status.Active >= 1 {
		return JobResult{Started: true}, true
	}

	for _, cond := range job.Status.Conditions {
		if cond.Status != "True" {
			continue
		}
		switch cond.Type {
		case batchv1.JobComplete:
			return JobResult{Started: true, Succeeded: true}, true
		case batchv1.JobFailed:
			// Kubernetes sets Reason="DeadlineExceeded" when activeDeadlineSeconds
			// is exceeded (Requirement 2.3: 10-minute TTL → BUILD_TIMEOUT).
			reason := types.SubmissionStatusBuildFailed
			if cond.Reason == "DeadlineExceeded" {
				reason = types.SubmissionStatusBuildTimeout
			}
			return JobResult{Started: true, Failed: true, Reason: reason}, true
		case batchv1.JobSuspended:
			// A suspended job has not started; keep watching.
		}
	}

	return JobResult{}, false
}
