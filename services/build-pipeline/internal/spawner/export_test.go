// export_test.go exposes test-only constructors for the spawner package.
// It is compiled only as part of the test binary for this package.
package spawner

import (
	"context"

	networkingv1 "k8s.io/api/networking/v1"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExportBuildNetworkPolicy exposes BuildNetworkPolicy for external test packages.
func ExportBuildNetworkPolicy(submissionID, namespace string) *networkingv1.NetworkPolicy {
	return BuildNetworkPolicy(submissionID, namespace)
}

// ExportApplyNetworkPolicy exposes ApplyNetworkPolicy for external test packages.
func ExportApplyNetworkPolicy(ctx context.Context, client kubernetes.Interface, submissionID, namespace string) error {
	return ApplyNetworkPolicy(ctx, client, submissionID, namespace)
}

// StatusUpdaterFunc is a callback that updates a submission status, allowing
// tests to inject a fake implementation instead of a real database pool.
type StatusUpdaterFunc func(ctx context.Context, submissionID, status string) error

// jobSpawnerWithFakeDB is a test-only variant of JobSpawner that accepts a
// status-updater callback in place of a real pgxpool.
type jobSpawnerWithFakeDB struct {
	client              kubernetes.Interface
	namespace           string
	artifactRegistryURL string
	logger              *zap.Logger
	updateStatus        StatusUpdaterFunc
}

// SpawnBuildJob calls the shared core implementation with the fake updater.
func (s *jobSpawnerWithFakeDB) SpawnBuildJob(ctx context.Context, submissionID, artifactURI string) error {
	return spawnBuildJobImpl(ctx, s.client, s.namespace, s.artifactRegistryURL,
		submissionID, artifactURI, s.logger, s.updateStatus)
}

// NewForTest returns a *JobSpawner suitable for tests that only test the
// success path (pool may be nil since it won't be called on success).
func NewForTest(
	client kubernetes.Interface,
	pool *pgxpool.Pool,
	namespace, artifactRegistryURL string,
	logger *zap.Logger,
) *JobSpawner {
	return New(client, pool, namespace, artifactRegistryURL, logger)
}

// NewForTestWithDB returns a spawner backed by the given updateStatus callback
// instead of a real database pool, allowing tests to assert which status was
// written on a job-creation failure.
func NewForTestWithDB(
	client kubernetes.Interface,
	namespace, artifactRegistryURL string,
	logger *zap.Logger,
	updateStatus StatusUpdaterFunc,
) interface {
	SpawnBuildJob(ctx context.Context, submissionID, artifactURI string) error
} {
	return &jobSpawnerWithFakeDB{
		client:              client,
		namespace:           namespace,
		artifactRegistryURL: artifactRegistryURL,
		logger:              logger,
		updateStatus:        updateStatus,
	}
}
