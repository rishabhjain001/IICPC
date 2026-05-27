// Package image assembles and pushes minimal OCI images for DBHP submissions
// using the buildah CLI.
//
// Requirements: 2.5, 2.6
package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// buildahBinary is the name of the buildah executable.  Tests override this
// via the DBHP_BUILDAH_BIN environment variable to inject a test double.
func buildahBinary() string {
	if v := os.Getenv("DBHP_BUILDAH_BIN"); v != "" {
		return v
	}
	return "buildah"
}

// BuildManifest is the reproducible build manifest written alongside the
// pushed image (Requirement 2.7).
type BuildManifest struct {
	SubmissionID      string            `json:"submission_id"`
	Toolchain         string            `json:"toolchain"`
	CompilerFlags     []string          `json:"compiler_flags"`
	DependencyHashes  map[string]string `json:"dependency_hashes"`
	BuildTimestampUTC string            `json:"build_timestamp_utc"`
	ImageDigest       string            `json:"image_digest"`
}

// BuildMinimalImage assembles a minimal OCI image using buildah and returns
// the full image reference (e.g.
// "registry.internal/submissions/abc-123:latest").
//
// Steps:
//  1. buildah from scratch  → workingContainerID
//  2. buildah copy binary   → /app/submission
//  3. buildah copy each dep → /lib/<basename>
//  4. buildah config --cmd  → ["/app/submission"]
//  5. buildah commit        → imageRef
//
// The registryURL parameter is the base registry URL, e.g.
// "registry.internal.iicpc.io/dbhp".  The returned imageRef is
// "<registryURL>/submissions/<submissionID>:latest".
func BuildMinimalImage(
	ctx context.Context,
	registryURL string,
	submissionID string,
	binaryPath string,
	deps []string,
) (string, error) {
	imageRef := fmt.Sprintf("%s/submissions/%s:latest", registryURL, submissionID)

	// 1. Create a working container from scratch.
	containerID, err := runBuildah(ctx, "from", "scratch")
	if err != nil {
		return "", fmt.Errorf("image: buildah from scratch: %w", err)
	}
	containerID = strings.TrimSpace(containerID)

	// 2. Copy the compiled binary to /app/submission.
	if _, err := runBuildah(ctx, "copy", containerID, binaryPath, "/app/submission"); err != nil {
		return "", fmt.Errorf("image: buildah copy binary: %w", err)
	}

	// 3. Copy each dynamic dependency to /lib/<basename>.
	for _, dep := range deps {
		dest := filepath.Join("/lib", filepath.Base(dep))
		if _, err := runBuildah(ctx, "copy", containerID, dep, dest); err != nil {
			return "", fmt.Errorf("image: buildah copy dep %s: %w", dep, err)
		}
	}

	// 4. Set the CMD so the container runs the submission binary.
	if _, err := runBuildah(ctx, "config", "--cmd", `["/app/submission"]`, containerID); err != nil {
		return "", fmt.Errorf("image: buildah config cmd: %w", err)
	}

	// 5. Commit the working container to produce the image.
	imageID, err := runBuildah(ctx, "commit", containerID, imageRef)
	if err != nil {
		return "", fmt.Errorf("image: buildah commit: %w", err)
	}
	_ = strings.TrimSpace(imageID) // imageID returned for the caller's reference

	return imageRef, nil
}

// WriteManifest computes dependency hashes and writes the reproducible build
// manifest as JSON to "<manifestDir>/<submissionID>.json".
//
// manifestDir is typically "<ARTIFACT_REGISTRY_DIR>/manifests".
func WriteManifest(
	manifestDir string,
	submissionID string,
	toolchain string,
	compilerFlags []string,
	depPaths []string,
	imageDigest string,
) error {
	depHashes, err := hashFiles(depPaths)
	if err != nil {
		return fmt.Errorf("image: hashing deps: %w", err)
	}

	// Ensure dependency_hashes is always an object (never JSON null) even when
	// there are no dynamic dependencies (e.g. statically linked Go binaries).
	if depHashes == nil {
		depHashes = map[string]string{}
	}

	// Ensure compiler_flags is always an array (never JSON null).
	if compilerFlags == nil {
		compilerFlags = []string{}
	}

	m := BuildManifest{
		SubmissionID:      submissionID,
		Toolchain:         toolchain,
		CompilerFlags:     compilerFlags,
		DependencyHashes:  depHashes,
		BuildTimestampUTC: time.Now().UTC().Format(time.RFC3339),
		ImageDigest:       imageDigest,
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("image: marshal manifest: %w", err)
	}

	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return fmt.Errorf("image: create manifest dir %s: %w", manifestDir, err)
	}

	outPath := filepath.Join(manifestDir, submissionID+".json")
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("image: write manifest %s: %w", outPath, err)
	}

	return nil
}

// hashFiles returns a map of filepath.Base(path) → "sha256:<hex>" for each
// file in paths.
func hashFiles(paths []string) (map[string]string, error) {
	result := make(map[string]string, len(paths))
	for _, p := range paths {
		h, err := hashFile(p)
		if err != nil {
			return nil, err
		}
		result[filepath.Base(p)] = "sha256:" + h
	}
	return result, nil
}

// hashFile returns the lowercase hex-encoded SHA-256 digest of the file at p.
func hashFile(p string) (string, error) {
	f, err := os.Open(p) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("image: open %s: %w", p, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("image: hash %s: %w", p, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// runBuildah executes the buildah CLI with the supplied arguments and returns
// the trimmed stdout on success.  On non-zero exit it returns an error
// containing the combined stdout+stderr.
func runBuildah(ctx context.Context, args ...string) (string, error) {
	bin := buildahBinary()
	//nolint:gosec // args are constructed internally; no user-controlled interpolation.
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("buildah %s: %w; stderr: %s",
			strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}
