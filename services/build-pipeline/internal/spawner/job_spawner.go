// Package spawner creates hermetic Kubernetes build Jobs for DBHP submissions.
package spawner

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/iicpc/dbhp/build-pipeline/internal/db"
	"github.com/iicpc/dbhp/shared-go/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// buildImage is the placeholder image used until Task 4.2 wires the real
	// build-runner image.
	buildImage = "dbhp-build-runner:latest"

	// maxJobNameLen is the Kubernetes limit for Job names.
	maxJobNameLen = 63
)

// JobSpawner holds the dependencies needed to create build Jobs.
type JobSpawner struct {
	client    kubernetes.Interface
	pool      *pgxpool.Pool
	namespace string
	logger    *zap.Logger
	// artifactRegistryURL is forwarded to the build container via env var.
	artifactRegistryURL string
}

// New constructs a JobSpawner.
func New(
	client kubernetes.Interface,
	pool *pgxpool.Pool,
	namespace string,
	artifactRegistryURL string,
	logger *zap.Logger,
) *JobSpawner {
	return &JobSpawner{
		client:              client,
		pool:                pool,
		namespace:           namespace,
		artifactRegistryURL: artifactRegistryURL,
		logger:              logger,
	}
}

// SpawnBuildJob creates a hermetic Kubernetes Job that builds the submission
// artifact identified by submissionID and stored at artifactURI.
//
// Security guarantees enforced by the Job spec:
//   - automountServiceAccountToken: false
//   - runAsNonRoot: true / runAsUser: 1000
//   - readOnlyRootFilesystem: true
//   - allowPrivilegeEscalation: false
//   - capabilities.drop: ["ALL"]
//   - activeDeadlineSeconds: 600 (10-minute build TTL per Requirement 2.3)
//   - backoffLimit: 0 (no retries)
//
// If Job creation fails the submission is marked BUILD_INFRASTRUCTURE_ERROR.
func (s *JobSpawner) SpawnBuildJob(ctx context.Context, submissionID string, artifactURI string) error {
	return spawnBuildJobImpl(
		ctx,
		s.client,
		s.namespace,
		s.artifactRegistryURL,
		submissionID,
		artifactURI,
		s.logger,
		func(ctx context.Context, id, status string) error {
			return db.UpdateSubmissionStatus(ctx, s.pool, id, status)
		},
	)
}

// spawnBuildJobImpl is the core implementation shared by JobSpawner and the
// test-only variant.  The updateStatus callback decouples the function from the
// concrete database pool, making it straightforward to unit-test.
func spawnBuildJobImpl(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, artifactRegistryURL string,
	submissionID, artifactURI string,
	logger *zap.Logger,
	updateStatus func(ctx context.Context, submissionID, status string) error,
) error {
	jobName := buildJobName(submissionID)

	logger.Info("spawning build job",
		zap.String("submission_id", submissionID),
		zap.String("job_name", jobName),
		zap.String("artifact_uri", artifactURI),
	)

	job := buildJobSpec(jobName, submissionID, artifactURI, artifactRegistryURL, namespace)

	_, err := client.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		logger.Error("failed to create build job",
			zap.String("submission_id", submissionID),
			zap.String("job_name", jobName),
			zap.Error(err),
		)

		// Mark the submission as infrastructure-error so the contestant and
		// control plane are informed (Requirement 2.1).
		if dbErr := updateStatus(ctx, submissionID,
			types.SubmissionStatusBuildInfrastructureError); dbErr != nil {
			logger.Error("failed to update submission status after job creation error",
				zap.String("submission_id", submissionID),
				zap.Error(dbErr),
			)
		}

		return fmt.Errorf("spawner: create job %s: %w", jobName, err)
	}

	logger.Info("build job created successfully",
		zap.String("submission_id", submissionID),
		zap.String("job_name", jobName),
	)
	return nil
}

// buildJobName returns a RFC-1123-compliant Job name for the given submission
// ID, truncated to 63 characters.
func buildJobName(submissionID string) string {
	name := "build-" + submissionID
	if len(name) > maxJobNameLen {
		name = name[:maxJobNameLen]
	}
	return name
}

// buildJobSpec assembles the full batch/v1 Job manifest.
func buildJobSpec(
	jobName, submissionID, artifactURI, artifactRegistryURL, namespace string,
) *batchv1.Job {
	var (
		backoffLimit          int32 = 0
		activeDeadlineSeconds int64 = 600 // 10-minute build TTL (Requirement 2.3)
		automount                   = false
		runAsUser             int64 = 1000
		runAsNonRoot                = true
		readOnly                    = true
		noPrivEsc                   = false
	)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels: map[string]string{
				"app":           "dbhp-build",
				"submission-id": submissionID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":           "dbhp-build",
						"submission-id": submissionID,
					},
				},
				Spec: corev1.PodSpec{
					// Disable automatic service-account token projection
					// (Requirement 2.2 — hermetic build container).
					AutomountServiceAccountToken: &automount,
					RestartPolicy:                corev1.RestartPolicyNever,

					// Pod-level security context: run as a non-root user.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &runAsNonRoot,
						RunAsUser:    &runAsUser,
					},

					Containers: []corev1.Container{
						{
							Name:  "build",
							Image: buildImage,

							// Container-level security context:
							// read-only root fs, no privilege escalation, all
							// Linux capabilities dropped (Requirement 2.2).
							SecurityContext: &corev1.SecurityContext{
								ReadOnlyRootFilesystem:   &readOnly,
								AllowPrivilegeEscalation: &noPrivEsc,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},

							Env: []corev1.EnvVar{
								{Name: "SUBMISSION_ID", Value: submissionID},
								{Name: "ARTIFACT_URI", Value: artifactURI},
								{Name: "ARTIFACT_REGISTRY_URL", Value: artifactRegistryURL},
							},

							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("8Gi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("8Gi"),
								},
							},
						},
					},
				},
			},
		},
	}
}
