package spawner_test

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/iicpc/dbhp/build-pipeline/internal/spawner"
	"github.com/iicpc/dbhp/shared-go/types"
)

// ---------------------------------------------------------------------------
// WatchBuildJob tests
// ---------------------------------------------------------------------------

// TestWatchBuildJob_ReturnsBuildInfrastructureErrorOnTimeout verifies that
// when the watch channel closes without the job becoming active, the watcher
// returns a Failed result with BUILD_INFRASTRUCTURE_ERROR reason.
func TestWatchBuildJob_ReturnsBuildInfrastructureErrorOnTimeout(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	// Inject a watcher that we control: we never send any events, simulating a
	// timeout by closing the channel after a short delay.
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	// Use a very short timeout so the test completes quickly.
	const timeoutSecs int64 = 1

	// Close the fake watcher immediately to trigger the "channel closed" path.
	go func() {
		time.Sleep(10 * time.Millisecond)
		fakeWatcher.Stop()
	}()

	result, err := spawner.WatchBuildJob(
		context.Background(), fakeClient,
		"build-some-submission", testNamespace, timeoutSecs,
	)

	// The watch channel was closed; no API-level error expected.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Failed {
		t.Error("expected result.Failed = true on timeout")
	}
	if result.Reason != types.SubmissionStatusBuildInfrastructureError {
		t.Errorf("expected reason %q, got %q",
			types.SubmissionStatusBuildInfrastructureError, result.Reason)
	}
	if result.Started {
		t.Error("expected result.Started = false on timeout")
	}
}

// TestWatchBuildJob_StartsWhenJobBecomesActive verifies that when the Job has
// at least one active pod, WatchBuildJob returns Started=true immediately.
func TestWatchBuildJob_StartsWhenJobBecomesActive(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	const timeoutSecs int64 = 5

	go func() {
		time.Sleep(20 * time.Millisecond)
		// Send a Modified event with Active = 1.
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "build-active-test",
				Namespace: testNamespace,
			},
			Status: batchv1.JobStatus{
				Active: 1,
			},
		}
		fakeWatcher.Modify(job)
	}()

	result, err := spawner.WatchBuildJob(
		context.Background(), fakeClient,
		"build-active-test", testNamespace, timeoutSecs,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Started {
		t.Error("expected result.Started = true when Active >= 1")
	}
	if result.Failed {
		t.Error("expected result.Failed = false when job becomes active")
	}
}

// TestWatchBuildJob_SucceedsWhenJobCompletes verifies that a Complete condition
// maps to Succeeded=true.
func TestWatchBuildJob_SucceedsWhenJobCompletes(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	go func() {
		time.Sleep(20 * time.Millisecond)
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "build-complete-test",
				Namespace: testNamespace,
			},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{
					{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}
		fakeWatcher.Modify(job)
	}()

	result, err := spawner.WatchBuildJob(
		context.Background(), fakeClient,
		"build-complete-test", testNamespace, 5,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected result.Succeeded = true on Complete condition")
	}
	if result.Failed {
		t.Error("expected result.Failed = false when job completes")
	}
	if !result.Started {
		t.Error("expected result.Started = true when job completes")
	}
}

// TestWatchBuildJob_FailsWhenJobFails verifies that a Failed condition maps to
// Failed=true with a reason.
func TestWatchBuildJob_FailsWhenJobFails(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	go func() {
		time.Sleep(20 * time.Millisecond)
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "build-failed-test",
				Namespace: testNamespace,
			},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{
					{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
						Reason: "BackoffLimitExceeded",
					},
				},
			},
		}
		fakeWatcher.Modify(job)
	}()

	result, err := spawner.WatchBuildJob(
		context.Background(), fakeClient,
		"build-failed-test", testNamespace, 5,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Failed {
		t.Error("expected result.Failed = true on Failed condition")
	}
	if result.Succeeded {
		t.Error("expected result.Succeeded = false on Failed condition")
	}
}

// TestWatchBuildJob_BuildTimeoutOnDeadlineExceeded verifies that a Failed
// condition with Reason=DeadlineExceeded maps to BUILD_TIMEOUT (Requirement 2.3).
func TestWatchBuildJob_BuildTimeoutOnDeadlineExceeded(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	go func() {
		time.Sleep(20 * time.Millisecond)
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "build-timeout-test",
				Namespace: testNamespace,
			},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{
					{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
						Reason: "DeadlineExceeded",
					},
				},
			},
		}
		fakeWatcher.Modify(job)
	}()

	result, err := spawner.WatchBuildJob(
		context.Background(), fakeClient,
		"build-timeout-test", testNamespace, 5,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Failed {
		t.Error("expected result.Failed = true on DeadlineExceeded")
	}
	if result.Reason != types.SubmissionStatusBuildTimeout {
		t.Errorf("expected reason %q on DeadlineExceeded, got %q",
			types.SubmissionStatusBuildTimeout, result.Reason)
	}
}

// TestWatchBuildJob_ContextCancellation verifies that WatchBuildJob returns
// BUILD_INFRASTRUCTURE_ERROR when the context is cancelled before the job
// becomes active.
func TestWatchBuildJob_ContextCancellation(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	result, err := spawner.WatchBuildJob(ctx, fakeClient, "build-cancel-test", testNamespace, 60)

	// Context cancellation returns a non-nil error (ctx.Err()).
	if err == nil {
		t.Error("expected non-nil error on context cancellation")
	}
	if !result.Failed {
		t.Error("expected result.Failed = true on context cancellation")
	}
	if result.Reason != types.SubmissionStatusBuildInfrastructureError {
		t.Errorf("expected reason %q, got %q",
			types.SubmissionStatusBuildInfrastructureError, result.Reason)
	}
}

// ---------------------------------------------------------------------------
// compile-time checks
// ---------------------------------------------------------------------------

var _ = runtime.Object((*batchv1.Job)(nil))
