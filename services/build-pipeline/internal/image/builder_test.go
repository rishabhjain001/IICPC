// builder_test.go verifies the OCI image assembly, push, and manifest paths.
//
// Requirements: 2.5, 2.6
package image_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/build-pipeline/internal/image"
	"github.com/iicpc/dbhp/shared-go/types"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// memStatusUpdater records the most-recently set status.
type memStatusUpdater struct {
	status string
}

func (m *memStatusUpdater) UpdateStatus(_ context.Context, _ string, status string) error {
	m.status = status
	return nil
}

// ---------------------------------------------------------------------------
// Buildah fake helper
// ---------------------------------------------------------------------------

// writeFakeBuildah writes a stub "buildah" executable to dir and returns its
// path. On Unix/Linux a shell script is written; on Windows a .bat file is
// written and the returned path includes the .bat extension so that
// exec.CommandContext can find it without relying on PATHEXT.
//
// The stub accepts the key sub-commands (from, copy, config, commit, push)
// and exits 0 for all except "push" when the environment variable
// FAKE_BUILDAH_PUSH_FAIL=1 is set (which causes it to exit 1).
//
// The stub prints a fixed container-ID on "from" and a fixed image-ID on
// "commit" so callers get predictable output.
func writeFakeBuildah(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// Write a Windows .bat stub.
		script := `@echo off
set cmd=%1
if "%cmd%"=="from" (
  echo working-container-id
  exit /b 0
)
if "%cmd%"=="copy" exit /b 0
if "%cmd%"=="config" exit /b 0
if "%cmd%"=="commit" (
  echo sha256:abc123imageID
  exit /b 0
)
if "%cmd%"=="push" (
  if "%FAKE_BUILDAH_PUSH_FAIL%"=="1" (
    echo push: simulated push failure 1>&2
    exit /b 1
  )
  exit /b 0
)
exit /b 1
`
		batPath := filepath.Join(dir, "buildah.bat")
		if err := os.WriteFile(batPath, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake buildah.bat: %v", err)
		}
		return batPath
	}
	// Unix shell script stub.
	script := `#!/bin/sh
cmd=$1
if [ "$cmd" = "from" ]; then
  echo "working-container-id"
  exit 0
fi
if [ "$cmd" = "copy" ]; then
  exit 0
fi
if [ "$cmd" = "config" ]; then
  exit 0
fi
if [ "$cmd" = "commit" ]; then
  echo "sha256:abc123imageID"
  exit 0
fi
if [ "$cmd" = "push" ]; then
  if [ "$FAKE_BUILDAH_PUSH_FAIL" = "1" ]; then
    echo "push: simulated push failure" >&2
    exit 1
  fi
  exit 0
fi
# Unknown sub-command
exit 1
`
	path := filepath.Join(dir, "buildah")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake buildah: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// BuildMinimalImage tests
// ---------------------------------------------------------------------------

// TestBuildMinimalImage_ReturnsImageRef verifies that BuildMinimalImage returns
// the expected full image reference when buildah calls succeed.
func TestBuildMinimalImage_ReturnsImageRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("buildah fake binary requires Unix shell; skipping on Windows")
	}
	// Create a temporary directory for the fake buildah binary.
	binDir := t.TempDir()
	buildahPath := writeFakeBuildah(t, binDir)
	t.Setenv("DBHP_BUILDAH_BIN", buildahPath)

	// Create a fake binary file for the copy step (buildah fake doesn't
	// actually stat the file, but we provide it for realism).
	workDir := t.TempDir()
	binaryPath := filepath.Join(workDir, "submission")
	if err := os.WriteFile(binaryPath, []byte("ELF"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	// Minimal dep file.
	depPath := filepath.Join(workDir, "libc.so.6")
	if err := os.WriteFile(depPath, []byte("so"), 0o644); err != nil {
		t.Fatalf("write fake dep: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	imageRef, err := image.BuildMinimalImage(
		ctx,
		"registry.internal/dbhp",
		"sub-abc-123",
		binaryPath,
		[]string{depPath},
	)
	if err != nil {
		t.Fatalf("BuildMinimalImage: unexpected error: %v", err)
	}

	expected := "registry.internal/dbhp/submissions/sub-abc-123:latest"
	if imageRef != expected {
		t.Errorf("expected image ref %q, got %q", expected, imageRef)
	}
}

// ---------------------------------------------------------------------------
// PushImage tests
// ---------------------------------------------------------------------------

// TestPushImage_SuccessDoesNotSetFailedStatus verifies that a successful push
// does not update the submission status.
func TestPushImage_SuccessDoesNotSetFailedStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("buildah fake binary requires Unix shell; skipping on Windows")
	}
	binDir := t.TempDir()
	buildahPath := writeFakeBuildah(t, binDir)
	t.Setenv("DBHP_BUILDAH_BIN", buildahPath)
	// Ensure push succeeds.
	t.Setenv("FAKE_BUILDAH_PUSH_FAIL", "0")

	updater := &memStatusUpdater{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := image.PushImage(ctx, "registry.internal/dbhp/submissions/sub-abc:latest",
		"sub-abc", updater, zap.NewNop())
	if err != nil {
		t.Fatalf("PushImage: unexpected error on successful push: %v", err)
	}
	if updater.status != "" {
		t.Errorf("expected no status update on success, got %q", updater.status)
	}
}

// TestPushImage_FailureSetsPublishFailed verifies that a push failure (non-zero
// buildah exit) sets the submission status to BUILD_PUBLISH_FAILED and returns
// a wrapped ErrPushFailed (Requirement 2.6).
func TestPushImage_FailureSetsPublishFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("buildah fake binary requires Unix shell; skipping on Windows")
	}
	binDir := t.TempDir()
	buildahPath := writeFakeBuildah(t, binDir)
	t.Setenv("DBHP_BUILDAH_BIN", buildahPath)
	// Force push to fail.
	t.Setenv("FAKE_BUILDAH_PUSH_FAIL", "1")

	updater := &memStatusUpdater{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := image.PushImage(ctx, "registry.internal/dbhp/submissions/sub-fail:latest",
		"sub-fail", updater, zap.NewNop())

	if err == nil {
		t.Fatal("expected an error from PushImage on push failure, got nil")
	}
	if !errors.Is(err, image.ErrPushFailed) {
		t.Errorf("expected wrapped ErrPushFailed, got: %v", err)
	}
	if updater.status != types.SubmissionStatusBuildPublishFailed {
		t.Errorf("expected status %q, got %q",
			types.SubmissionStatusBuildPublishFailed, updater.status)
	}
}

// ---------------------------------------------------------------------------
// WriteManifest / manifest JSON tests
// ---------------------------------------------------------------------------

// TestWriteManifest_WellFormed verifies that the manifest JSON written to disk
// is valid JSON and contains the expected fields (Requirement 2.7).
func TestWriteManifest_WellFormed(t *testing.T) {
	dir := t.TempDir()

	depFile := filepath.Join(dir, "libc.so.6")
	if err := os.WriteFile(depFile, []byte("fake shared object"), 0o644); err != nil {
		t.Fatalf("write dep file: %v", err)
	}

	err := image.WriteManifest(
		filepath.Join(dir, "manifests"),
		"sub-xyz",
		"rustc-1.77.0",
		[]string{"--release"},
		[]string{depFile},
		"sha256:deadbeef",
	)
	if err != nil {
		t.Fatalf("WriteManifest: unexpected error: %v", err)
	}

	outPath := filepath.Join(dir, "manifests", "sub-xyz.json")
	data, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("read manifest file: %v", readErr)
	}

	var m image.BuildManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\ncontent: %s", err, string(data))
	}

	// Validate required fields.
	if m.SubmissionID != "sub-xyz" {
		t.Errorf("submission_id: expected %q, got %q", "sub-xyz", m.SubmissionID)
	}
	if m.Toolchain != "rustc-1.77.0" {
		t.Errorf("toolchain: expected %q, got %q", "rustc-1.77.0", m.Toolchain)
	}
	if len(m.CompilerFlags) != 1 || m.CompilerFlags[0] != "--release" {
		t.Errorf("compiler_flags: expected [--release], got %v", m.CompilerFlags)
	}
	if m.ImageDigest != "sha256:deadbeef" {
		t.Errorf("image_digest: expected %q, got %q", "sha256:deadbeef", m.ImageDigest)
	}
	if m.BuildTimestampUTC == "" {
		t.Error("build_timestamp_utc must not be empty")
	}
	// Timestamp should be parseable as RFC3339.
	if _, err := time.Parse(time.RFC3339, m.BuildTimestampUTC); err != nil {
		t.Errorf("build_timestamp_utc is not RFC3339: %v", err)
	}
	// Dependency hash should be present and well-formed.
	hash, ok := m.DependencyHashes["libc.so.6"]
	if !ok {
		t.Error("dependency_hashes must contain libc.so.6")
	}
	if len(hash) < 7 || hash[:7] != "sha256:" {
		t.Errorf("dependency hash should start with 'sha256:', got %q", hash)
	}
}

// TestWriteManifest_NoDepsEmptyHashMap verifies that a binary with no dynamic
// deps produces an empty (not nil) dependency_hashes object.
func TestWriteManifest_NoDepsEmptyHashMap(t *testing.T) {
	dir := t.TempDir()

	err := image.WriteManifest(
		filepath.Join(dir, "manifests"),
		"sub-go-static",
		"go1.22",
		[]string{"./..."},
		nil, // no dynamic deps
		"sha256:ffee",
	)
	if err != nil {
		t.Fatalf("WriteManifest: unexpected error: %v", err)
	}

	outPath := filepath.Join(dir, "manifests", "sub-go-static.json")
	data, _ := os.ReadFile(outPath)

	var m image.BuildManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if m.DependencyHashes == nil {
		t.Error("dependency_hashes should be an empty map, not null")
	}
	if len(m.DependencyHashes) != 0 {
		t.Errorf("expected empty dependency_hashes, got %v", m.DependencyHashes)
	}
}

// ---------------------------------------------------------------------------
// Integration-style: push failure after successful build sets BUILD_PUBLISH_FAILED
// ---------------------------------------------------------------------------

// stubPublisher implements executor.PostBuildPublisher, delegating the push
// step to image.PushImage. It wires a fake buildah binary to make push fail.
//
// This test is purposely exercised within the image package to validate the
// full round-trip: build → push failure → status BUILD_PUBLISH_FAILED.
func TestPushFailureAfterBuild_SetsPublishFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("buildah fake binary requires Unix shell; skipping on Windows")
	}
	binDir := t.TempDir()
	buildahPath := writeFakeBuildah(t, binDir)
	t.Setenv("DBHP_BUILDAH_BIN", buildahPath)
	t.Setenv("FAKE_BUILDAH_PUSH_FAIL", "1")

	workDir := t.TempDir()
	binaryPath := filepath.Join(workDir, "submission")
	if err := os.WriteFile(binaryPath, []byte("ELF"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	updater := &memStatusUpdater{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Simulate the post-build pipeline: BuildMinimalImage succeeds, PushImage fails.
	imageRef, buildErr := image.BuildMinimalImage(ctx, "registry.internal/dbhp", "sub-x", binaryPath, nil)
	if buildErr != nil {
		t.Fatalf("BuildMinimalImage unexpectedly failed: %v", buildErr)
	}

	pushErr := image.PushImage(ctx, imageRef, "sub-x", updater, zap.NewNop())
	if pushErr == nil {
		t.Fatal("expected push to fail")
	}

	if !errors.Is(pushErr, image.ErrPushFailed) {
		t.Errorf("expected ErrPushFailed, got: %v", pushErr)
	}
	if updater.status != types.SubmissionStatusBuildPublishFailed {
		t.Errorf("expected status BUILD_PUBLISH_FAILED, got %q", updater.status)
	}
	_ = fmt.Sprintf("verified: push failure sets %s", types.SubmissionStatusBuildPublishFailed)
}
