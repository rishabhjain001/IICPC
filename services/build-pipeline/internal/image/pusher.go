package image

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/shared-go/types"
)

// ErrPushFailed is returned by PushImage when buildah push exits non-zero.
// Callers can check for this sentinel with errors.Is to distinguish a push
// failure from an infrastructure-level error.
var ErrPushFailed = errors.New("OCI image push failed")

// StatusUpdater persists a new submission status to the data store.
// The same interface is already defined in executor; re-declared here so the
// image package does not import executor and create a cycle.
type StatusUpdater interface {
	UpdateStatus(ctx context.Context, submissionID, status string) error
}

// PushImage runs "buildah push <imageRef>" to upload the image to the
// Artifact Registry.
//
// On non-zero exit:
//  1. The submission status is updated to BUILD_PUBLISH_FAILED via updater.
//  2. A wrapped ErrPushFailed is returned so callers can distinguish push
//     failures from other errors.
//
// On success, nil is returned and the caller is responsible for setting the
// final BUILT status and storing the image digest.
func PushImage(
	ctx context.Context,
	imageRef string,
	submissionID string,
	updater StatusUpdater,
	logger *zap.Logger,
) error {
	log := logger.With(
		zap.String("submission_id", submissionID),
		zap.String("image_ref", imageRef),
	)

	log.Info("pushing OCI image")

	if _, err := runBuildah(ctx, "push", imageRef); err != nil {
		log.Error("buildah push failed", zap.Error(err))

		// Mark the submission BUILD_PUBLISH_FAILED (Requirement 2.6).
		if updateErr := updater.UpdateStatus(
			ctx, submissionID, types.SubmissionStatusBuildPublishFailed,
		); updateErr != nil {
			log.Error("failed to update status to BUILD_PUBLISH_FAILED",
				zap.Error(updateErr),
			)
			return fmt.Errorf("%w: %v; additionally, status update failed: %v",
				ErrPushFailed, err, updateErr)
		}

		return fmt.Errorf("%w: %v", ErrPushFailed, err)
	}

	log.Info("OCI image pushed successfully")
	return nil
}
