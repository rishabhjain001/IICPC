// executor.go wires together manifest detection, toolchain selection, build
// execution, log capture, and status update into a single cohesive BuildJob.
//
// Requirements: 2.2, 2.3, 2.4, 2.5, 2.6
package executor

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/shared-go/types"
)

// StatusUpdater is a callback that persists a new submission status to the
// data store.  It is kept as an interface to make BuildJob testable without a
// live database pool.
type StatusUpdater interface {
	UpdateStatus(ctx context.Context, submissionID, status string) error
}

// StatusUpdaterFunc is a function adapter for StatusUpdater.
type StatusUpdaterFunc func(ctx context.Context, submissionID, status string) error

// UpdateStatus implements StatusUpdater.
func (f StatusUpdaterFunc) UpdateStatus(ctx context.Context, submissionID, status string) error {
	return f(ctx, submissionID, status)
}

// LogStorer is a callback that stores a build log for a given submission.
// The log has already been middle-truncated to at most MaxLogBytes.
type LogStorer interface {
	StoreLog(ctx context.Context, submissionID string, log []byte) error
}

// LogStorerFunc is a function adapter for LogStorer.
type LogStorerFunc func(ctx context.Context, submissionID string, log []byte) error

// StoreLog implements LogStorer.
func (f LogStorerFunc) StoreLog(ctx context.Context, submissionID string, log []byte) error {
	return f(ctx, submissionID, log)
}

// PostBuildPublisher is called by BuildJob after a successful compile to
// assemble and push the minimal OCI image to the Artifact Registry.
//
// Implementations should:
//  1. Enumerate dynamic library dependencies from the compiled binary.
//  2. Assemble a minimal OCI image via buildah and push it to the registry.
//  3. Write the reproducible build manifest alongside the image.
//  4. On push failure, set the submission status to BUILD_PUBLISH_FAILED and
//     return a wrapped ErrPushFailed.
//  5. On success, set the submission status to BUILT and return the image
//     digest so it can be stored in the database.
//
// Requirements: 2.5, 2.6
type PostBuildPublisher interface {
	// Publish performs the full post-build pipeline for the given binary.
	// binaryPath is the absolute path to the compiled binary.
	// manifestType is the string value of the ManifestType constant used
	// during the build (used to choose the dep-enumeration strategy).
	// Returns the OCI image digest on success, or an error on failure.
	Publish(ctx context.Context, submissionID, binaryPath string, manifestType ManifestType) (imageDigest string, err error)
}

// ImageDigestStorer persists the OCI image digest for a submission after a
// successful build and push.
type ImageDigestStorer interface {
	StoreImageDigest(ctx context.Context, submissionID, digest string) error
}

// ImageDigestStorerFunc is a function adapter for ImageDigestStorer.
type ImageDigestStorerFunc func(ctx context.Context, submissionID, digest string) error

// StoreImageDigest implements ImageDigestStorer.
func (f ImageDigestStorerFunc) StoreImageDigest(ctx context.Context, submissionID, digest string) error {
	return f(ctx, submissionID, digest)
}

// BuildJob orchestrates a single submission build:
//  1. Detects the manifest type from the extracted archive root.
//  2. Selects the correct pinned toolchain command.
//  3. Runs the build with a 10-minute TTL.
//  4. Captures and middle-truncates stdout+stderr.
//  5. Updates submission status to BUILD_TIMEOUT / BUILD_FAILED / BUILT.
//  6. Stores the build log via LogStorer for non-successful builds.
//  7. On success, calls PostBuildPublisher to assemble + push the OCI image.
//  8. Stores the image digest via ImageDigestStorer on a successful push.
type BuildJob struct {
	// WorkDir is the directory containing the extracted submission archive.
	// Build commands are executed from this directory.
	WorkDir string

	// SubmissionID is the UUID of the submission being built.
	SubmissionID string

	// StatusUpdater persists the final build status.
	StatusUpdater StatusUpdater

	// LogStorer persists the build log on failure (may be nil for successful
	// builds where no log storage is required by the caller).
	LogStorer LogStorer

	// Publisher is called after a successful compile to assemble and push the
	// OCI image.  When nil the build job completes with status BUILT without
	// image assembly (useful in unit tests that only test the compile path).
	Publisher PostBuildPublisher

	// ImageDigestStorer persists the image digest returned by Publisher.
	// May be nil when Publisher is nil.
	ImageDigestStorer ImageDigestStorer

	// Logger is used to emit structured build lifecycle events.
	Logger *zap.Logger
}

// Execute performs the full build lifecycle for the submission:
//
//  1. List files in WorkDir and detect the manifest type.
//  2. Select the build command for that manifest.
//  3. Run the build (10-minute TTL via RunBuild).
//  4. On timeout → status BUILD_TIMEOUT, store log.
//  5. On non-zero exit → status BUILD_FAILED, store log.
//  6. On compile success, if a Publisher is configured:
//     a. Enumerate dynamic deps and assemble + push the minimal OCI image.
//     b. On push failure → status BUILD_PUBLISH_FAILED (set by Publisher).
//     c. On push success → store image digest, set status BUILT.
//  7. If no Publisher is configured → status BUILT immediately.
//
// Execute always returns nil for build-level failures; all such failures are
// encoded in status updates so callers do not need extra error handling.
// Infrastructure errors (cannot list files, cannot detect manifest, etc.) are
// returned as regular Go errors.
func (b *BuildJob) Execute(ctx context.Context) error {
	logger := b.Logger.With(zap.String("submission_id", b.SubmissionID))

	// ------------------------------------------------------------------ detect
	files, err := listRootFiles(b.WorkDir)
	if err != nil {
		return fmt.Errorf("executor: list files in %s: %w", b.WorkDir, err)
	}

	manifest := DetectManifest(files)
	if manifest == ManifestUnknown {
		// No recognised manifest — treat as a build failure so the contestant
		// receives actionable feedback.
		log := []byte("error: no recognised build manifest (Makefile, Cargo.toml, or go.mod) found at the archive root\n")
		logger.Warn("no recognised manifest; marking BUILD_FAILED")
		return b.fail(ctx, log, types.SubmissionStatusBuildFailed)
	}

	logger.Info("manifest detected", zap.String("manifest", string(manifest)))

	// ------------------------------------------------------------- select cmd
	cmd, err := BuildCommandFor(manifest)
	if err != nil {
		return fmt.Errorf("executor: build command for manifest %s: %w", manifest, err)
	}

	// ------------------------------------------------------------------- run
	logger.Info("starting build",
		zap.String("binary", cmd.Binary),
		zap.Strings("args", cmd.Args),
	)

	result := RunBuild(ctx, b.WorkDir, cmd)

	logger.Info("build completed",
		zap.Int("exit_code", result.ExitCode),
		zap.Bool("timed_out", result.TimedOut),
		zap.Int("log_bytes", len(result.Log)),
	)

	// ---------------------------------------------------------------- outcome
	if result.TimedOut {
		logger.Warn("build exceeded 10-minute TTL; setting BUILD_TIMEOUT")
		return b.fail(ctx, result.Log, types.SubmissionStatusBuildTimeout)
	}

	if result.ExitCode != 0 {
		logger.Warn("build exited with non-zero code; setting BUILD_FAILED",
			zap.Int("exit_code", result.ExitCode),
		)
		return b.fail(ctx, result.Log, types.SubmissionStatusBuildFailed)
	}

	// ----------------------------------------------------- post-build publish
	// If a PostBuildPublisher is wired in, assemble and push the OCI image
	// before marking the submission BUILT (Requirements 2.5, 2.6).
	if b.Publisher != nil {
		logger.Info("compile succeeded; starting OCI image assembly and push")

		imageDigest, publishErr := b.Publisher.Publish(ctx, b.SubmissionID, b.WorkDir, manifest)
		if publishErr != nil {
			// The Publisher is responsible for setting the correct terminal
			// status (BUILD_PUBLISH_FAILED for push failures, or similar for
			// other errors) before returning.  We log the error and stop the
			// job; we do NOT set BUILT when publishing fails.
			logger.Error("post-build publish failed", zap.Error(publishErr))
			return nil
		}

		// Store the image digest in the database.
		if b.ImageDigestStorer != nil {
			if err := b.ImageDigestStorer.StoreImageDigest(ctx, b.SubmissionID, imageDigest); err != nil {
				logger.Error("failed to store image digest",
					zap.String("digest", imageDigest),
					zap.Error(err),
				)
				// Non-fatal: the image was pushed successfully; continue.
			}
		}

		logger.Info("OCI image pushed; setting BUILT", zap.String("digest", imageDigest))
		if err := b.StatusUpdater.UpdateStatus(ctx, b.SubmissionID, types.SubmissionStatusBuilt); err != nil {
			logger.Error("failed to update status to BUILT", zap.Error(err))
			return fmt.Errorf("executor: update status BUILT: %w", err)
		}
		return nil
	}

	// ------------------------------------------------------------- success (no publisher)
	logger.Info("build succeeded; setting BUILT")
	if err := b.StatusUpdater.UpdateStatus(ctx, b.SubmissionID, types.SubmissionStatusBuilt); err != nil {
		logger.Error("failed to update status to BUILT", zap.Error(err))
		return fmt.Errorf("executor: update status BUILT: %w", err)
	}
	return nil
}

// fail stores the build log (if a LogStorer is configured) and updates the
// submission status to the given terminal failure status.
func (b *BuildJob) fail(ctx context.Context, log []byte, status string) error {
	if b.LogStorer != nil {
		if err := b.LogStorer.StoreLog(ctx, b.SubmissionID, log); err != nil {
			b.Logger.Error("failed to store build log",
				zap.String("submission_id", b.SubmissionID),
				zap.Error(err),
			)
			// Non-fatal: continue to update status even if log storage fails.
		}
	}
	if err := b.StatusUpdater.UpdateStatus(ctx, b.SubmissionID, status); err != nil {
		b.Logger.Error("failed to update submission status",
			zap.String("submission_id", b.SubmissionID),
			zap.String("status", status),
			zap.Error(err),
		)
		return fmt.Errorf("executor: update status %s: %w", status, err)
	}
	return nil
}

// listRootFiles returns the base names of all entries at the top level of dir.
// Only the file names (not paths) are returned, matching the convention
// expected by DetectManifest.
func listRootFiles(dir string) ([]string, error) {
	pattern := filepath.Join(dir, "*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, filepath.Base(m))
	}
	sort.Strings(names)
	return names, nil
}
