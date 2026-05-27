package registry_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iicpc/dbhp/submission-engine/internal/registry"
)

// ---------------------------------------------------------------------------
// HTTPClient — PushArtifact
// ---------------------------------------------------------------------------

// TestHTTPClient_PushArtifact_Success verifies that a successful registry
// response (2xx) causes PushArtifact to return a non-empty URI and nil error.
func TestHTTPClient_PushArtifact_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/v2/submissions/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	client := registry.NewHTTPClient(srv.URL)

	data := []byte("fake-binary-data")
	uri, err := client.PushArtifact(context.Background(), "sub-123", "ELF_BINARY", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("PushArtifact returned unexpected error: %v", err)
	}
	if uri == "" {
		t.Fatal("PushArtifact returned empty URI on success")
	}
	if !strings.Contains(uri, "sub-123") {
		t.Errorf("URI %q does not contain submission ID", uri)
	}
}

// TestHTTPClient_PushArtifact_RegistryUnavailable_5xx verifies that an HTTP
// 5xx response causes ErrRegistryUnavailable (Requirement 1.10).
func TestHTTPClient_PushArtifact_RegistryUnavailable_5xx(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		code := code
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
			defer srv.Close()

			client := registry.NewHTTPClient(srv.URL)
			data := []byte("some data")
			_, err := client.PushArtifact(context.Background(), "sub-5xx", "ELF_BINARY", bytes.NewReader(data), int64(len(data)))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, registry.ErrRegistryUnavailable) {
				t.Errorf("expected ErrRegistryUnavailable, got %v", err)
			}
		})
	}
}

// TestHTTPClient_PushArtifact_RegistryUnavailable_ConnectionRefused verifies
// that a connection-refused network error maps to ErrRegistryUnavailable.
func TestHTTPClient_PushArtifact_RegistryUnavailable_ConnectionRefused(t *testing.T) {
	// Port 19999 should not be listening; if it is, this test may mis-pass.
	client := registry.NewHTTPClient("http://127.0.0.1:19999")

	data := []byte("data")
	_, err := client.PushArtifact(context.Background(), "sub-refused", "ELF_BINARY", bytes.NewReader(data), int64(len(data)))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, registry.ErrRegistryUnavailable) {
		t.Errorf("expected ErrRegistryUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// HTTPClient — PushOCIImage
// ---------------------------------------------------------------------------

// TestHTTPClient_PushOCIImage_Success verifies PushOCIImage returns a URI on success.
func TestHTTPClient_PushOCIImage_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	client := registry.NewHTTPClient(srv.URL)
	uri, err := client.PushOCIImage(context.Background(), "sub-oci", "sha256:abc123")
	if err != nil {
		t.Fatalf("PushOCIImage returned unexpected error: %v", err)
	}
	if uri == "" {
		t.Fatal("PushOCIImage returned empty URI on success")
	}
}

// TestHTTPClient_PushOCIImage_RegistryUnavailable verifies that a 5xx causes
// ErrRegistryUnavailable from PushOCIImage.
func TestHTTPClient_PushOCIImage_RegistryUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := registry.NewHTTPClient(srv.URL)
	_, err := client.PushOCIImage(context.Background(), "sub-oci-fail", "latest")
	if !errors.Is(err, registry.ErrRegistryUnavailable) {
		t.Errorf("expected ErrRegistryUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// FSClient — PushArtifact
// ---------------------------------------------------------------------------

// TestFSClient_PushArtifact_Success verifies that FSClient writes the blob to
// disk and returns a file:// URI.
func TestFSClient_PushArtifact_Success(t *testing.T) {
	dir := t.TempDir()
	client := registry.NewFSClient(dir)

	data := []byte("hello world blob")
	uri, err := client.PushArtifact(context.Background(), "sub-fs-1", "ELF_BINARY", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("PushArtifact returned unexpected error: %v", err)
	}
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("expected file:// URI, got %q", uri)
	}

	// Verify the blob was actually written with the correct content.
	blobPath := filepath.Join(dir, "submissions", "sub-fs-1", "blob")
	got, readErr := os.ReadFile(blobPath)
	if readErr != nil {
		t.Fatalf("blob file not created: %v", readErr)
	}
	if string(got) != string(data) {
		t.Errorf("blob content mismatch: got %q, want %q", got, data)
	}
}

// TestFSClient_PushArtifact_ArtifactTypeMetadata verifies that artifact_type
// metadata is stored alongside the blob.
func TestFSClient_PushArtifact_ArtifactTypeMetadata(t *testing.T) {
	dir := t.TempDir()
	client := registry.NewFSClient(dir)

	data := []byte("binary")
	_, err := client.PushArtifact(context.Background(), "sub-meta", "ELF_BINARY", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	metaPath := filepath.Join(dir, "submissions", "sub-meta", "artifact_type")
	got, readErr := os.ReadFile(metaPath)
	if readErr != nil {
		t.Fatalf("artifact_type file not created: %v", readErr)
	}
	if string(got) != "ELF_BINARY" {
		t.Errorf("artifact_type mismatch: got %q", got)
	}
}

// TestFSClient_PushArtifact_BadDir verifies that an unwritable path causes
// ErrRegistryUnavailable.
func TestFSClient_PushArtifact_BadDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere; skipping permission test")
	}
	// Create a regular file and try to use it as a directory.
	tmpFile, err := os.CreateTemp("", "reg-test-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	client := registry.NewFSClient(filepath.Join(tmpFile.Name(), "nested"))

	data := []byte("data")
	_, err = client.PushArtifact(context.Background(), "sub-bad", "ELF_BINARY", bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, registry.ErrRegistryUnavailable) {
		t.Errorf("expected ErrRegistryUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// FSClient — PushOCIImage
// ---------------------------------------------------------------------------

// TestFSClient_PushOCIImage_Success verifies FSClient writes the image ref.
func TestFSClient_PushOCIImage_Success(t *testing.T) {
	dir := t.TempDir()
	client := registry.NewFSClient(dir)

	uri, err := client.PushOCIImage(context.Background(), "sub-fs-oci", "sha256:deadbeef")
	if err != nil {
		t.Fatalf("PushOCIImage returned unexpected error: %v", err)
	}
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("expected file:// URI, got %q", uri)
	}

	refPath := filepath.Join(dir, "submissions", "sub-fs-oci", "image_ref")
	got, readErr := os.ReadFile(refPath)
	if readErr != nil {
		t.Fatalf("image_ref file not created: %v", readErr)
	}
	if string(got) != "sha256:deadbeef" {
		t.Errorf("image_ref content mismatch: got %q", got)
	}
}
