// Package registry provides Artifact Registry clients for the Submission Engine.
//
// Two implementations are provided:
//   - HTTPClient  — talks to an OCI distribution-spec v1 registry over HTTP(S).
//   - FSClient    — stores blobs on the local filesystem (demo / local dev mode).
//
// Both satisfy the Client interface. The factory function New picks an
// implementation based on environment variables at startup.
package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrRegistryUnavailable is returned by Client methods when the registry
// cannot be reached or returns an HTTP 5xx response.
//
// Per Requirement 1.10 the upload handler MUST NOT assign a Submission ID when
// it receives this error.
var ErrRegistryUnavailable = errors.New("artifact registry unavailable")

// Client is the interface the upload handler uses to store artifacts.
// Decoupling via an interface enables test doubles without a real registry.
type Client interface {
	// PushArtifact pushes a raw binary blob to the registry, tagging it with
	// submissionID. data is the full artifact payload; size is its byte length.
	//
	// Returns:
	//   - (artifactURI, nil)           — success; URI identifies the blob.
	//   - ("", ErrRegistryUnavailable) — registry unreachable or 5xx.
	//   - ("", err)                    — other hard failures.
	PushArtifact(ctx context.Context, submissionID string, artifactType string, data io.Reader, size int64) (artifactURI string, err error)

	// PushOCIImage records an OCI image reference (imageRef) in the registry
	// associated with submissionID. Used by the Build Pipeline after a
	// successful build to push the assembled image tag.
	//
	// Returns the same sentinel errors as PushArtifact.
	PushOCIImage(ctx context.Context, submissionID string, imageRef string) (artifactURI string, err error)
}

// ---------------------------------------------------------------------------
// HTTPClient — OCI distribution-spec v1
// ---------------------------------------------------------------------------

// HTTPClient is a production Client that talks to an OCI distribution-spec v1
// registry over plain HTTP(S) using the standard library net/http.
type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewHTTPClient returns an HTTPClient configured to push to the registry at
// baseURL (e.g. "http://registry:5000"). A 30-second timeout is applied so
// that Requirement 1.8 (store within 30 s of acknowledgment) is enforced.
func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// PushArtifact uploads data to:
//
//	PUT {baseURL}/v2/submissions/{submissionID}/blobs/uploads/
//
// On success it returns the canonical URI
// "{baseURL}/v2/submissions/{submissionID}".
func (c *HTTPClient) PushArtifact(
	ctx context.Context,
	submissionID string,
	artifactType string,
	data io.Reader,
	size int64,
) (string, error) {
	url := fmt.Sprintf("%s/v2/submissions/%s/blobs/uploads/", c.baseURL, submissionID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, data)
	if err != nil {
		return "", fmt.Errorf("registry: build request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	if artifactType != "" {
		req.Header.Set("X-Artifact-Type", artifactType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Network errors (refused, timeout, DNS) → registry unavailable.
		return "", ErrRegistryUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return "", ErrRegistryUnavailable
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry: unexpected status %d", resp.StatusCode)
	}

	artifactURI := fmt.Sprintf("%s/v2/submissions/%s", c.baseURL, submissionID)
	return artifactURI, nil
}

// PushOCIImage records an OCI image tag reference via:
//
//	PUT {baseURL}/v2/submissions/{submissionID}/manifests/{imageRef}
func (c *HTTPClient) PushOCIImage(
	ctx context.Context,
	submissionID string,
	imageRef string,
) (string, error) {
	// For demo purposes the image reference is stored as an opaque manifest.
	url := fmt.Sprintf("%s/v2/submissions/%s/manifests/%s", c.baseURL, submissionID, imageRef)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader([]byte(imageRef)))
	if err != nil {
		return "", fmt.Errorf("registry: build OCI request: %w", err)
	}
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", ErrRegistryUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return "", ErrRegistryUnavailable
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry: OCI push unexpected status %d", resp.StatusCode)
	}

	artifactURI := fmt.Sprintf("%s/v2/submissions/%s/manifests/%s", c.baseURL, submissionID, imageRef)
	return artifactURI, nil
}

// ---------------------------------------------------------------------------
// FSClient — filesystem registry (local / demo mode)
// ---------------------------------------------------------------------------

// FSClient stores artifacts as files on the local filesystem. It is intended
// for local development and demo environments where an OCI registry is not
// available.
//
// Blobs are stored at:
//
//	{baseDir}/submissions/{submissionID}/blob
//
// OCI image references are stored at:
//
//	{baseDir}/submissions/{submissionID}/image_ref
type FSClient struct {
	baseDir string
}

// NewFSClient returns an FSClient that stores files under baseDir.
func NewFSClient(baseDir string) *FSClient {
	return &FSClient{baseDir: baseDir}
}

// PushArtifact writes data to {baseDir}/submissions/{submissionID}/blob.
// The directory is created if it does not exist.
func (c *FSClient) PushArtifact(
	ctx context.Context,
	submissionID string,
	artifactType string,
	data io.Reader,
	size int64,
) (string, error) {
	dir := filepath.Join(c.baseDir, "submissions", submissionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", ErrRegistryUnavailable
	}

	blobPath := filepath.Join(dir, "blob")
	f, err := os.Create(blobPath)
	if err != nil {
		return "", ErrRegistryUnavailable
	}
	defer f.Close()

	if _, err := io.Copy(f, data); err != nil {
		return "", ErrRegistryUnavailable
	}

	// Also store the artifact type as metadata.
	if artifactType != "" {
		metaPath := filepath.Join(dir, "artifact_type")
		_ = os.WriteFile(metaPath, []byte(artifactType), 0o644)
	}

	artifactURI := fmt.Sprintf("file://%s", blobPath)
	return artifactURI, nil
}

// PushOCIImage writes imageRef to {baseDir}/submissions/{submissionID}/image_ref.
func (c *FSClient) PushOCIImage(
	ctx context.Context,
	submissionID string,
	imageRef string,
) (string, error) {
	dir := filepath.Join(c.baseDir, "submissions", submissionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", ErrRegistryUnavailable
	}

	refPath := filepath.Join(dir, "image_ref")
	if err := os.WriteFile(refPath, []byte(imageRef), 0o644); err != nil {
		return "", ErrRegistryUnavailable
	}

	artifactURI := fmt.Sprintf("file://%s", refPath)
	return artifactURI, nil
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

// New returns a Client configured from environment variables:
//
//   - If ARTIFACT_REGISTRY_DIR is set (non-empty), returns an FSClient.
//   - Otherwise, reads ARTIFACT_REGISTRY_URL (default "http://localhost:5000")
//     and returns an HTTPClient.
func New() Client {
	if dir := os.Getenv("ARTIFACT_REGISTRY_DIR"); dir != "" {
		return NewFSClient(dir)
	}
	url := os.Getenv("ARTIFACT_REGISTRY_URL")
	if url == "" {
		url = "http://localhost:5000"
	}
	return NewHTTPClient(url)
}
