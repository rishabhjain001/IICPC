package validator_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/iicpc/dbhp/submission-engine/internal/validator"
)

// ---- helpers ----

// sha256Hex computes the hex-encoded SHA-256 of b.
func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// elfBytes returns a minimal ELF magic-byte payload.
func elfBytes() []byte {
	payload := make([]byte, 64)
	payload[0] = 0x7f
	payload[1] = 'E'
	payload[2] = 'L'
	payload[3] = 'F'
	return payload
}

// tarGzWith creates an in-memory .tar.gz archive containing one file at entryPath.
func tarGzWith(entryPath, content string) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	body := []byte(content)
	hdr := &tar.Header{
		Name:     entryPath,
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		panic(err)
	}
	if _, err := tw.Write(body); err != nil {
		panic(err)
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

// zipWith creates an in-memory .zip archive containing one file at entryPath.
func zipWith(entryPath, content string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(entryPath)
	if err != nil {
		panic(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		panic(err)
	}
	zw.Close()
	return buf.Bytes()
}

// ---- tests ----

func TestValidELF(t *testing.T) {
	data := elfBytes()
	r := bytes.NewReader(data)
	res, got, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Type != validator.ArtifactELF {
		t.Errorf("expected ArtifactELF, got %q", res.Type)
	}
	if res.Size != int64(len(data)) {
		t.Errorf("expected size %d, got %d", len(data), res.Size)
	}
	if !bytes.Equal(got, data) {
		t.Error("returned bytes do not match input")
	}
}

func TestValidTarGzWithCargoToml(t *testing.T) {
	data := tarGzWith("myproject/Cargo.toml", `[package]\nname = "foo"\nversion = "0.1.0"`)
	r := bytes.NewReader(data)
	res, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Type != validator.ArtifactArchive {
		t.Errorf("expected ArtifactArchive, got %q", res.Type)
	}
}

func TestValidTarGzWithMakefile(t *testing.T) {
	data := tarGzWith("Makefile", "all:\n\tgcc -o prog main.c\n")
	r := bytes.NewReader(data)
	res, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Type != validator.ArtifactArchive {
		t.Errorf("expected ArtifactArchive, got %q", res.Type)
	}
}

func TestValidZipWithGoMod(t *testing.T) {
	data := zipWith("go.mod", "module example.com/foo\n\ngo 1.22\n")
	r := bytes.NewReader(data)
	res, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Type != validator.ArtifactArchive {
		t.Errorf("expected ArtifactArchive, got %q", res.Type)
	}
}

func TestValidZipWithCargoToml(t *testing.T) {
	data := zipWith("Cargo.toml", "[package]\nname = \"mybot\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	r := bytes.NewReader(data)
	res, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != nil {
		t.Fatalf("unexpected error for zip with root Cargo.toml: %v", err)
	}
	if res.Type != validator.ArtifactArchive {
		t.Errorf("expected ArtifactArchive, got %q", res.Type)
	}
}

func TestValidZipWithMakefileOneLevelDeep(t *testing.T) {
	data := zipWith("proj/Makefile", "all:\n\techo ok\n")
	r := bytes.NewReader(data)
	res, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Type != validator.ArtifactArchive {
		t.Errorf("expected ArtifactArchive, got %q", res.Type)
	}
}

func TestOversizedContent(t *testing.T) {
	// Build a reader that emits MaxArtifactBytes+1 bytes without materialising
	// the full buffer in memory (which would OOM on 32-bit processes).
	// The first 4 bytes are valid ELF magic; the remainder are zeros served
	// by an io.LimitedReader wrapping an infinite zero source.
	elfMagic := []byte{0x7f, 'E', 'L', 'F'}
	// An io.Reader that returns infinite zero bytes.
	zeroReader := &infiniteZeroReader{}
	// Limit to exactly (MaxArtifactBytes+1 - 4) bytes after the magic header.
	limitedZeros := &io.LimitedReader{R: zeroReader, N: int64(validator.MaxArtifactBytes - 3)}
	multi := io.MultiReader(bytes.NewReader(elfMagic), limitedZeros)
	_, _, err := validator.ValidateArtifact(multi, "")
	if err != validator.ErrTooLarge {
		t.Errorf("expected ErrTooLarge, got %v", err)
	}
}

// infiniteZeroReader is an io.Reader that always fills p with zero bytes.
type infiniteZeroReader struct{}

func (r *infiniteZeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestUnknownFormat(t *testing.T) {
	// PDF header — not ELF, tar.gz, or zip.
	data := []byte("%PDF-1.4 some content here padding to 64 bytes padding padding pad")
	r := bytes.NewReader(data)
	_, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != validator.ErrBadFormat {
		t.Errorf("expected ErrBadFormat, got %v", err)
	}
}

func TestTarGzWithoutRootManifest(t *testing.T) {
	// Archive has a file but no Makefile / Cargo.toml / go.mod at root.
	data := tarGzWith("src/main.rs", "fn main() {}")
	r := bytes.NewReader(data)
	_, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != validator.ErrBadFormat {
		t.Errorf("expected ErrBadFormat for missing root manifest, got %v", err)
	}
}

func TestZipWithoutRootManifest(t *testing.T) {
	data := zipWith("src/main.go", "package main\nfunc main() {}\n")
	r := bytes.NewReader(data)
	_, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != validator.ErrBadFormat {
		t.Errorf("expected ErrBadFormat for missing root manifest, got %v", err)
	}
}

func TestTarGzManifestTooDeep(t *testing.T) {
	// Manifest is two levels deep — should be rejected.
	data := tarGzWith("a/b/Makefile", "all:\n")
	r := bytes.NewReader(data)
	_, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != validator.ErrBadFormat {
		t.Errorf("expected ErrBadFormat for deeply nested manifest, got %v", err)
	}
}

func TestCorrectChecksum(t *testing.T) {
	data := elfBytes()
	r := bytes.NewReader(data)
	_, _, err := validator.ValidateArtifact(r, sha256Hex(data))
	if err != nil {
		t.Errorf("unexpected error for correct checksum: %v", err)
	}
}

func TestWrongChecksum(t *testing.T) {
	data := elfBytes()
	r := bytes.NewReader(data)
	_, _, err := validator.ValidateArtifact(r, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != validator.ErrBadChecksum {
		t.Errorf("expected ErrBadChecksum, got %v", err)
	}
}

func TestCaseInsensitiveChecksum(t *testing.T) {
	data := elfBytes()
	upper := strings.ToUpper(sha256Hex(data))
	r := bytes.NewReader(data)
	_, _, err := validator.ValidateArtifact(r, upper)
	if err != nil {
		t.Errorf("expected case-insensitive checksum to pass, got %v", err)
	}
}

// ---- Tests for DetectFormat ----

func TestDetectFormatELF(t *testing.T) {
	data := elfBytes()
	artType, got, err := validator.DetectFormat(bytes.NewReader(data), "binary")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if artType != validator.ArtifactELF {
		t.Errorf("expected ArtifactELF, got %q", artType)
	}
	if !bytes.Equal(got, data) {
		t.Error("returned bytes do not match input")
	}
}

func TestDetectFormatArchiveTarGz(t *testing.T) {
	data := tarGzWith("Cargo.toml", "[package]\nname=\"foo\"")
	artType, _, err := validator.DetectFormat(bytes.NewReader(data), "submission.tar.gz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if artType != validator.ArtifactArchive {
		t.Errorf("expected ArtifactArchive, got %q", artType)
	}
}

func TestDetectFormatArchiveZip(t *testing.T) {
	data := zipWith("go.mod", "module foo\n\ngo 1.22\n")
	artType, _, err := validator.DetectFormat(bytes.NewReader(data), "submission.zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if artType != validator.ArtifactArchive {
		t.Errorf("expected ArtifactArchive, got %q", artType)
	}
}

func TestDetectFormatTarGzMissingManifest(t *testing.T) {
	data := tarGzWith("src/main.rs", "fn main(){}")
	_, _, err := validator.DetectFormat(bytes.NewReader(data), "submission.tar.gz")
	if err != validator.ErrBadFormat {
		t.Errorf("expected ErrBadFormat, got %v", err)
	}
}

func TestDetectFormatUnknown(t *testing.T) {
	data := []byte("%PDF-1.4 content padding padding padding padding padding padding pad")
	_, _, err := validator.DetectFormat(bytes.NewReader(data), "doc.pdf")
	if err != validator.ErrBadFormat {
		t.Errorf("expected ErrBadFormat, got %v", err)
	}
}

func TestDetectFormatTooLarge(t *testing.T) {
	elfMagic := []byte{0x7f, 'E', 'L', 'F'}
	// Use a streaming zero reader rather than materialising 500 MB in memory.
	limitedZeros := &io.LimitedReader{R: &infiniteZeroReader{}, N: int64(validator.MaxArtifactBytes - 3)}
	multi := io.MultiReader(bytes.NewReader(elfMagic), limitedZeros)
	_, _, err := validator.DetectFormat(multi, "big.elf")
	if err != validator.ErrTooLarge {
		t.Errorf("expected ErrTooLarge, got %v", err)
	}
}

// ---- Tests for VerifyChecksum ----

func TestVerifyChecksumMatch(t *testing.T) {
	data := elfBytes()
	if err := validator.VerifyChecksum(data, sha256Hex(data)); err != nil {
		t.Errorf("expected no error for matching checksum, got %v", err)
	}
}

func TestVerifyChecksumMismatch(t *testing.T) {
	data := elfBytes()
	wrong := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if err := validator.VerifyChecksum(data, wrong); err != validator.ErrBadChecksum {
		t.Errorf("expected ErrBadChecksum for mismatch, got %v", err)
	}
}

func TestVerifyChecksumEmptyHex(t *testing.T) {
	data := elfBytes()
	if err := validator.VerifyChecksum(data, ""); err != validator.ErrBadChecksum {
		t.Errorf("expected ErrBadChecksum for empty hex, got %v", err)
	}
}

func TestVerifyChecksumShortHex(t *testing.T) {
	data := elfBytes()
	if err := validator.VerifyChecksum(data, "abc123"); err != validator.ErrBadChecksum {
		t.Errorf("expected ErrBadChecksum for short hex, got %v", err)
	}
}

func TestVerifyChecksumCaseInsensitive(t *testing.T) {
	data := elfBytes()
	upper := strings.ToUpper(sha256Hex(data))
	if err := validator.VerifyChecksum(data, upper); err != nil {
		t.Errorf("expected case-insensitive checksum to pass, got %v", err)
	}
}
