package publisher

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/build-pipeline/internal/executor"
	"github.com/iicpc/dbhp/shared-go/types"
)

// Publisher assembles a minimal OCI image from a compiled binary and its
// dynamic library dependencies using buildah, then pushes it to the Artifact
// Registry.
//
// Requirements: 2.5, 2.6
type Publisher struct {
	// registryBase is the base URL of the Artifact Registry, e.g.
	// "registry.internal.iicpc.io/dbhp".  The full image reference is
	// "<registryBase>/submissions:<submissionID>".
	registryBase string

	// statusUpdater persists status transitions for the submission.
	statusUpdater executor.StatusUpdater

	// logger emits structured events during the publish lifecycle.
	logger *zap.Logger
}

// NewPublisher creates a Publisher backed by the given Artifact Registry base
// URL and status updater.
func NewPublisher(registryBase string, updater executor.StatusUpdater, logger *zap.Logger) *Publisher {
	return &Publisher{
		registryBase:  registryBase,
		statusUpdater: updater,
		logger:        logger,
	}
}

// Publish assembles and pushes a minimal OCI image for the given submission.
//
// The image is built from scratch using buildah:
//  1. Create a working container from "scratch".
//  2. Copy the compiled binary to /app/submission inside the container.
//  3. Copy each dynamic dependency to its original absolute path.
//  4. Set the container entrypoint to ["/app/submission"].
//  5. Commit the container as <registryBase>/submissions:<submissionID>.
//  6. Push the image to the registry.
//
// On any non-zero exit or error, the submission status is set to
// BUILD_PUBLISH_FAILED and the error is returned to the caller.
func (p *Publisher) Publish(ctx context.Context, submissionID, binaryPath string, depPaths []string) error {
	logger := p.logger.With(zap.String("submission_id", submissionID))
	imageRef := fmt.Sprintf("%s/submissions:%s", p.registryBase, submissionID)

	// Step 1: buildah from scratch → containerID
	containerID, err := p.buildahFrom(ctx, "scratch")
	if err != nil {
		logger.Error("buildah from scratch failed", zap.Error(err))
		return p.publishFailed(ctx, submissionID, fmt.Errorf("publisher: buildah from: %w", err))
	}
	logger.Info("created working container", zap.String("container_id", containerID))

	// Step 2: copy binary to /app/submission
	if err := p.buildahCopy(ctx, containerID, binaryPath, "/app/submission"); err != nil {
		logger.Error("buildah copy binary failed", zap.Error(err))
		return p.publishFailed(ctx, submissionID, fmt.Errorf("publisher: copy binary: %w", err))
	}

	// Step 3: copy each dep to its original absolute path (preserve layout)
	for _, dep := range depPaths {
		if err := p.buildahCopy(ctx, containerID, dep, dep); err != nil {
			logger.Error("buildah copy dep failed", zap.String("dep", dep), zap.Error(err))
			return p.publishFailed(ctx, submissionID, fmt.Errorf("publisher: copy dep %s: %w", dep, err))
		}
	}

	// Step 4: set entrypoint
	if err := p.buildahConfig(ctx, containerID, `["/app/submission"]`); err != nil {
		logger.Error("buildah config entrypoint failed", zap.Error(err))
		return p.publishFailed(ctx, submissionID, fmt.Errorf("publisher: config entrypoint: %w", err))
	}

	// Step 5: commit → image
	if _, err := p.buildahCommit(ctx, containerID, imageRef); err != nil {
		logger.Error("buildah commit failed", zap.Error(err))
		return p.publishFailed(ctx, submissionID, fmt.Errorf("publisher: commit image: %w", err))
	}
	logger.Info("committed image", zap.String("image_ref", imageRef))

	// Step 6: push
	if err := p.buildahPush(ctx, imageRef); err != nil {
		logger.Error("buildah push failed", zap.Error(err))
		return p.publishFailed(ctx, submissionID, fmt.Errorf("publisher: push image: %w", err))
	}
	logger.Info("image pushed successfully", zap.String("image_ref", imageRef))

	return nil
}

// publishFailed sets the submission status to BUILD_PUBLISH_FAILED and
// returns the original error to propagate to the caller.
func (p *Publisher) publishFailed(ctx context.Context, submissionID string, cause error) error {
	if updateErr := p.statusUpdater.UpdateStatus(
		ctx, submissionID, types.SubmissionStatusBuildPublishFailed,
	); updateErr != nil {
		p.logger.Error("failed to update status to BUILD_PUBLISH_FAILED",
			zap.String("submission_id", submissionID),
			zap.Error(updateErr),
		)
		// Surface both errors.
		return fmt.Errorf("%w; additionally, status update failed: %v", cause, updateErr)
	}
	return cause
}

// ---- buildah wrappers -------------------------------------------------------

// buildahFrom runs "buildah from <image>" and returns the container ID.
func (p *Publisher) buildahFrom(ctx context.Context, image string) (string, error) {
	out, err := p.runBuildah(ctx, "from", image)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// buildahCopy runs "buildah copy <containerID> <src> <dest>".
func (p *Publisher) buildahCopy(ctx context.Context, containerID, src, dest string) error {
	_, err := p.runBuildah(ctx, "copy", containerID, src, dest)
	return err
}

// buildahConfig runs "buildah config --entrypoint <entrypoint> <containerID>".
func (p *Publisher) buildahConfig(ctx context.Context, containerID, entrypoint string) error {
	_, err := p.runBuildah(ctx, "config", "--entrypoint", entrypoint, containerID)
	return err
}

// buildahCommit runs "buildah commit <containerID> <imageRef>" and returns
// the image ID emitted by buildah on stdout.
func (p *Publisher) buildahCommit(ctx context.Context, containerID, imageRef string) (string, error) {
	out, err := p.runBuildah(ctx, "commit", containerID, imageRef)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// buildahPush runs "buildah push <imageRef>".
func (p *Publisher) buildahPush(ctx context.Context, imageRef string) error {
	_, err := p.runBuildah(ctx, "push", imageRef)
	return err
}

// runBuildah executes the buildah CLI with the given arguments, returning
// combined stdout or an error if the command exits non-zero.
func (p *Publisher) runBuildah(ctx context.Context, args ...string) ([]byte, error) {
	//nolint:gosec // args are constructed internally; no user-controlled interpolation.
	cmd := exec.CommandContext(ctx, "buildah", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("buildah %s: %w; stderr: %s",
			strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}
