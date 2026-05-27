package spawner_test

import (
	"context"
	"errors"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"go.uber.org/zap"

	"github.com/iicpc/dbhp/build-pipeline/internal/spawner"
	"github.com/iicpc/dbhp/shared-go/types"
)

const (
	testNamespace           = "dbhp-builds"
	testArtifactRegistryURL = "https://registry.internal:5000"
)

// ---------------------------------------------------------------------------
// fakeDB is a minimal in-memory status store used in place of a real pgxpool.
// ---------------------------------------------------------------------------

// statusStore records the last status written per submission ID.
type statusStore struct {
	statuses map[string]string
}

func newStatusStore() *statusStore { return &statusStore{statuses: map[string]string{}} }

func (s *statusStore) set(id, status string) { s.statuses[id] = status }
func (s *statusStore) get(id string) string   { return s.statuses[id] }

// ---------------------------------------------------------------------------
// dbStubPool implements a minimal DB pool interface accepted by spawner.New
// via a thin adapter.  We pass nil for the real pool and intercept calls via
// the spawnerWithFakeDB helper below.
// ---------------------------------------------------------------------------

// TestSpawnBuildJob_CreatesJobWithCorrectSpec verifies that when the Kubernetes
// API call succeeds, the created Job has all required security fields set.
func TestSpawnBuildJob_CreatesJobWithCorrectSpec(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	logger := zap.NewNop()

	// Use nil pool; we won't hit a real DB in the success path.
	s := spawner.NewForTest(fakeClient, nil, testNamespace, testArtifactRegistryURL, logger)

	submissionID := "550e8400-e29b-41d4-a716-446655440000"
	artifactURI := "oci://registry.internal:5000/artifacts/550e8400"

	err := s.SpawnBuildJob(context.Background(), submissionID, artifactURI)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Retrieve the created Job from the fake client.
	jobName := "build-" + submissionID
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	job, err := fakeClient.BatchV1().Jobs(testNamespace).Get(
		context.Background(), jobName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("expected job %q to exist, got: %v", jobName, err)
	}

	// --- automountServiceAccountToken must be explicitly false ---
	if job.Spec.Template.Spec.AutomountServiceAccountToken == nil ||
		*job.Spec.Template.Spec.AutomountServiceAccountToken != false {
		t.Error("automountServiceAccountToken should be false")
	}

	// --- pod security context: non-root ---
	podSC := job.Spec.Template.Spec.SecurityContext
	if podSC == nil {
		t.Fatal("pod SecurityContext must not be nil")
	}
	if podSC.RunAsNonRoot == nil || !*podSC.RunAsNonRoot {
		t.Error("runAsNonRoot should be true")
	}
	if podSC.RunAsUser == nil || *podSC.RunAsUser != 1000 {
		t.Errorf("runAsUser should be 1000, got %v", podSC.RunAsUser)
	}

	// --- container security context ---
	if len(job.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("expected at least one container")
	}
	csc := job.Spec.Template.Spec.Containers[0].SecurityContext
	if csc == nil {
		t.Fatal("container SecurityContext must not be nil")
	}
	if csc.ReadOnlyRootFilesystem == nil || !*csc.ReadOnlyRootFilesystem {
		t.Error("readOnlyRootFilesystem should be true")
	}
	if csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
		t.Error("allowPrivilegeEscalation should be false")
	}
	if csc.Capabilities == nil {
		t.Fatal("capabilities must not be nil")
	}
	if len(csc.Capabilities.Drop) == 0 || csc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("capabilities.drop should contain ALL, got %v", csc.Capabilities.Drop)
	}

	// --- job spec: backoffLimit = 0 and activeDeadlineSeconds = 600 ---
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Errorf("backoffLimit should be 0, got %v", job.Spec.BackoffLimit)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 600 {
		t.Errorf("activeDeadlineSeconds should be 600 (10-minute build TTL), got %v", job.Spec.ActiveDeadlineSeconds)
	}

	// --- restartPolicy = Never ---
	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy should be Never, got %v", job.Spec.Template.Spec.RestartPolicy)
	}

	// --- labels ---
	assertLabel(t, job, "app", "dbhp-build")
	assertLabel(t, job, "submission-id", submissionID)

	// --- resource limits ---
	limits := job.Spec.Template.Spec.Containers[0].Resources.Limits
	if limits.Cpu().IsZero() {
		t.Error("cpu limit should not be zero")
	}
	if limits.Memory().IsZero() {
		t.Error("memory limit should not be zero")
	}

	// --- env vars ---
	assertEnvVar(t, job, "SUBMISSION_ID", submissionID)
	assertEnvVar(t, job, "ARTIFACT_URI", artifactURI)
	assertEnvVar(t, job, "ARTIFACT_REGISTRY_URL", testArtifactRegistryURL)
}

// TestSpawnBuildJob_SetsInfrastructureErrorOnFailure verifies that when the
// Kubernetes API returns an error, the spawner updates the submission status to
// BUILD_INFRASTRUCTURE_ERROR.
func TestSpawnBuildJob_SetsInfrastructureErrorOnFailure(t *testing.T) {
	t.Parallel()

	store := newStatusStore()
	fakeClient := fake.NewSimpleClientset()

	// Inject a reactor that makes every Jobs.Create call fail.
	fakeClient.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, k8serrors.NewInternalError(errors.New("scheduler unavailable"))
	})

	logger := zap.NewNop()

	// Build a spawner backed by the fake DB store.
	s := spawner.NewForTestWithDB(
		fakeClient,
		testNamespace,
		testArtifactRegistryURL,
		logger,
		func(_ context.Context, submissionID, status string) error {
			store.set(submissionID, status)
			return nil
		},
	)

	submissionID := "aaaabbbb-0000-0000-0000-000000000001"

	err := s.SpawnBuildJob(context.Background(), submissionID, "oci://registry/artifact")
	if err == nil {
		t.Fatal("expected an error when Job creation fails")
	}

	got := store.get(submissionID)
	if got != types.SubmissionStatusBuildInfrastructureError {
		t.Errorf("expected status %q, got %q",
			types.SubmissionStatusBuildInfrastructureError, got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertLabel(t *testing.T, job *batchv1.Job, key, want string) {
	t.Helper()
	if got := job.Labels[key]; got != want {
		t.Errorf("job label %q: want %q, got %q", key, want, got)
	}
}

func assertEnvVar(t *testing.T, job *batchv1.Job, name, want string) {
	t.Helper()
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			if e.Value != want {
				t.Errorf("env %q: want %q, got %q", name, want, e.Value)
			}
			return
		}
	}
	t.Errorf("env var %q not found in container spec", name)
}

// ensure the fake client schema knows about batch/v1 Jobs — verified at compile time.
var _ = batchv1.Job{}
