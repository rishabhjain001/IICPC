// Package validator provides artifact validation for the Submission Engine.
// It handles format detection (ELF / tar.gz / zip), archive root-manifest
// inspection, size cap enforcement, and SHA-256 checksum verification.
package validator

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// MaxArtifactBytes is the hard upper bound on accepted artifact size (500 MB).
const MaxArtifactBytes = 500 * 1024 * 1024

// ArtifactType identifies the kind of submission artifact.
type ArtifactType string

const (
	// ArtifactELF is a pre-compiled ELF binary (C++, Rust, or Go).
	ArtifactELF ArtifactType = "ELF_BINARY"
	// ArtifactArchive is a source archive (.tar.gz or .zip) with a root manifest.
	ArtifactArchive ArtifactType = "SOURCE_ARCHIVE"
)

// Sentinel errors returned by ValidateArtifact.
// The HTTP handler maps these to the appropriate status codes.
var (
	// ErrTooLarge is returned when the artifact exceeds MaxArtifactBytes.
	// HTTP 413.
	ErrTooLarge = errors.New("artifact exceeds 500 MB size limit")

	// ErrBadFormat is returned when the artifact is not a recognised format
	// or an archive that lacks a required root-level manifest file.
	// HTTP 415.
	ErrBadFormat = errors.New("unsupported or invalid artifact format")

	// ErrBadChecksum is returned when the computed SHA-256 digest does not
	// match the contestant-supplied expectedSHA256Hex.
	// HTTP 422; caller must delete any partial data.
	ErrBadChecksum = errors.New("SHA-256 checksum mismatch")
)

// ValidationResult holds the outcome of a successful validation pass.
type ValidationResult struct {
	// Type is ArtifactELF or ArtifactArchive.
	Type ArtifactType
	// Size is the total byte count of the artifact.
	Size int64
}

// rootManifests is the set of filenames that qualify as a root manifest inside
// a source archive.
var rootManifests = map[string]bool{
	"Makefile":   true,
	"Cargo.toml": true,
	"go.mod":     true,
}

// isRootManifest returns true if entryPath represents a root-level manifest.
// "Root-level" means the file is at the top of the archive (no directory
// separator) OR exactly one level deep (e.g. "myproject/Makefile").
func isRootManifest(entryPath string) bool {
	// Normalise to forward slashes and strip any leading/trailing slashes.
	cleaned := path.Clean(strings.ReplaceAll(entryPath, "\\", "/"))
	cleaned = strings.TrimLeft(cleaned, "/")

	base := path.Base(cleaned)
	if !rootManifests[base] {
		return false
	}

	// Count path components to allow at most one directory prefix.
	parts := strings.Split(cleaned, "/")
	return len(parts) <= 2
}

// DetectFormat reads all bytes from r (up to MaxArtifactBytes+1) and
// determines the artifact type. It returns:
//   - (ArtifactELF, nil)     — valid ELF binary
//   - (ArtifactArchive, nil) — valid source archive with a root manifest
//   - ("", ErrTooLarge)      — payload exceeds 500 MB
//   - ("", ErrBadFormat)     — unrecognised format or missing root manifest
//
// The raw bytes are also returned so callers can reuse them without re-reading.
func DetectFormat(r io.Reader, filename string) (ArtifactType, []byte, error) {
	limited := io.LimitReader(r, MaxArtifactBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", nil, fmt.Errorf("reading artifact: %w", err)
	}
	if int64(len(data)) > MaxArtifactBytes {
		return "", nil, ErrTooLarge
	}
	if len(data) < 4 {
		return "", nil, ErrBadFormat
	}

	switch {
	case isELF(data):
		return ArtifactELF, data, nil
	case isTarGz(data):
		if !archiveHasRootManifest_TarGz(data) {
			return "", nil, ErrBadFormat
		}
		return ArtifactArchive, data, nil
	case isZip(data):
		if !archiveHasRootManifest_Zip(data) {
			return "", nil, ErrBadFormat
		}
		return ArtifactArchive, data, nil
	default:
		return "", nil, ErrBadFormat
	}
}

// VerifyChecksum computes the SHA-256 digest of data and compares it against
// expectedHex (case-insensitive hex string). Returns ErrBadChecksum on
// mismatch or if expectedHex is empty or not a valid 64-character hex string.
func VerifyChecksum(data []byte, expectedHex string) error {
	if len(expectedHex) != 64 {
		return ErrBadChecksum
	}
	sum := sha256.Sum256(data)
	actualHex := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actualHex, expectedHex) {
		return ErrBadChecksum
	}
	return nil
}

// ValidateArtifact reads up to MaxArtifactBytes+1 bytes from r, detects the
// artifact format, verifies the SHA-256 checksum, and returns the result.
//
// The entire artifact is buffered so that:
//   - Size enforcement can stop early without reading the whole stream.
//   - Checksum can be computed over the full bytes once.
//   - Archive inspection can re-read from the buffer.
//
// The returned []byte is the complete artifact payload; callers may persist it
// directly. On error the slice may be nil or partial.
//
// Error mapping for HTTP handlers:
//   - ErrTooLarge     → 413
//   - ErrBadFormat    → 415
//   - ErrBadChecksum  → 422 (caller must delete partial data)
func ValidateArtifact(r io.Reader, expectedSHA256Hex string) (ValidationResult, []byte, error) {
	// Detect format and buffer all bytes.
	artifactType, data, err := DetectFormat(r, "")
	if err != nil {
		return ValidationResult{}, nil, err
	}

	// Verify SHA-256 checksum.
	if err := VerifyChecksum(data, expectedSHA256Hex); err != nil {
		return ValidationResult{}, nil, err
	}

	result := ValidationResult{
		Type: artifactType,
		Size: int64(len(data)),
	}
	return result, data, nil
}

// ---- Magic-byte helpers ----

// isELF returns true if data starts with the ELF magic bytes.
func isELF(data []byte) bool {
	return len(data) >= 4 &&
		data[0] == 0x7f &&
		data[1] == 'E' &&
		data[2] == 'L' &&
		data[3] == 'F'
}

// isTarGz returns true if data starts with the gzip magic bytes (0x1f 0x8b).
func isTarGz(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// isZip returns true if data starts with the PK zip local-file magic (50 4b 03 04).
func isZip(data []byte) bool {
	return len(data) >= 4 &&
		data[0] == 0x50 &&
		data[1] == 0x4b &&
		data[2] == 0x03 &&
		data[3] == 0x04
}

// ---- Archive root-manifest inspection ----

// archiveHasRootManifest_TarGz decompresses the gzip stream and walks tar
// headers looking for a root-level manifest file.
func archiveHasRootManifest_TarGz(data []byte) bool {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if isRootManifest(hdr.Name) {
			return true
		}
	}
	return false
}

// archiveHasRootManifest_Zip reads the zip central directory (from the
// in-memory bytes) and checks for a root-level manifest entry.
func archiveHasRootManifest_Zip(data []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if isRootManifest(f.Name) {
			return true
		}
	}
	return false
}
