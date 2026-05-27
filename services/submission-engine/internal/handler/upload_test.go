package handler_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/submission-engine/internal/db"
	"github.com/iicpc/dbhp/submission-engine/internal/handler"
	"github.com/iicpc/dbhp/submission-engine/internal/middleware"
	"github.com/iicpc/dbhp/submission-engine/internal/registry"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// uploadRateLimiter always allows uploads.
type uploadRateLimiter struct{}

func (s *uploadRateLimiter) Allow(_ context.Context, _ string) (bool, int64, error) {
	return true, 0, nil
}

// successRegistry always returns a fake URI with no error.
type successRegistry struct{}

func (s *successRegistry) PushArtifact(_ context.Context, submissionID, _ string, _ io.Reader, _ int64) (string, error) {
	return "file://test/" + submissionID, nil
}

func (s *successRegistry) PushOCIImage(_ context.Context, submissionID, imageRef string) (string, error) {
	return "file://test/" + submissionID + "/" + imageRef, nil
}

// unavailableRegistry always returns ErrRegistryUnavailable.
type unavailableRegistry struct{}

func (s *unavailableRegistry) PushArtifact(_ context.Context, _, _ string, _ io.Reader, _ int64) (string, error) {
	return "", registry.ErrRegistryUnavailable
}

func (s *unavailableRegistry) PushOCIImage(_ context.Context, _, _ string) (string, error) {
	return "", registry.ErrRegistryUnavailable
}

// uploadQuerier is a db.Querier test double for upload handler tests.
// LookupContestantByTokenHash always approves; InsertSubmission echoes the ID.
type uploadQuerier struct {
	insertErr error
}

func (s *uploadQuerier) LookupContestantByTokenHash(_ context.Context, _ []byte) (string, error) {
	return "contestant-uuid-upload", nil
}

func (s *uploadQuerier) InsertSubmission(_ context.Context, params db.InsertSubmissionParams) (string, error) {
	if s.insertErr != nil {
		return "", s.insertErr
	}
	return params.ID, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// minimalELF returns 64 bytes starting with the ELF magic number.
func minimalELF() []byte {
	data := make([]byte, 64)
	data[0] = 0x7f
	data[1] = 'E'
	data[2] = 'L'
	data[3] = 'F'
	return data
}

// hexSHA256 returns the lowercase hex SHA-256 digest of data.
func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// buildMultipart creates a multipart/form-data body with one "artifact" field.
// Returns (body, contentType).
func buildMultipart(t *testing.T, payload []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("artifact", "submission.bin")
	if err != nil {
		t.Fatalf("createFormFile: %v", err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

// postUpload fires POST /v1/submissions with the given payload and checksum.
// If checksumHex is "", the X-Checksum-SHA256 header is omitted.
func postUpload(
	t *testing.T,
	h http.Handler,
	payload []byte,
	checksumHex string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := buildMultipart(t, payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/submissions", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer valid-token")
	if checksumHex != "" {
		req.Header.Set("X-Checksum-SHA256", checksumHex)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// newUploadHandler wires Auth middleware → Upload handler with the provided
// registry and querier.
func newUploadHandler(reg registry.Client, q db.Querier) http.Handler {
	logger := zap.NewNop()
	lim := &uploadRateLimiter{}
	authMW := middleware.Auth(q, logger)
	return authMW(handler.Upload(logger, lim, reg, q))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestUploadHandler_201_WithSubmissionID verifies that a valid upload with a
// working registry returns HTTP 201 and a UUID submission_id (Req 1.9).
func TestUploadHandler_201_WithSubmissionID(t *testing.T) {
	payload := minimalELF()
	checksum := hexSHA256(payload)
	h := newUploadHandler(&successRegistry{}, &uploadQuerier{})

	rr := postUpload(t, h, payload, checksum)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		SubmissionID string `json:"submission_id"`
		Status       string `json:"status"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SubmissionID == "" {
		t.Error("submission_id must not be empty in a 201 response")
	}
	// UUID v4 is exactly 36 characters (32 hex + 4 dashes).
	if len(resp.SubmissionID) != 36 {
		t.Errorf("submission_id %q does not look like a UUID (want 36 chars)", resp.SubmissionID)
	}
	if resp.Status != "UPLOADED" {
		t.Errorf("status: want UPLOADED, got %q", resp.Status)
	}
}

// TestUploadHandler_503_NoSubmissionID verifies that when the registry is
// unavailable, the handler returns HTTP 503 with no submission_id (Req 1.10).
func TestUploadHandler_503_NoSubmissionID(t *testing.T) {
	payload := minimalELF()
	checksum := hexSHA256(payload)
	h := newUploadHandler(&unavailableRegistry{}, &uploadQuerier{})

	rr := postUpload(t, h, payload, checksum)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d; body: %s", rr.Code, rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["submission_id"]; ok {
		t.Error("submission_id MUST NOT be present in a 503 response (Requirement 1.10)")
	}
	if body["error"] != "REGISTRY_UNAVAILABLE" {
		t.Errorf("error: want REGISTRY_UNAVAILABLE, got %v", body["error"])
	}
}

// TestUploadHandler_503_IsJSON verifies the 503 body is valid JSON with the
// correct Content-Type.
func TestUploadHandler_503_IsJSON(t *testing.T) {
	payload := minimalELF()
	checksum := hexSHA256(payload)
	h := newUploadHandler(&unavailableRegistry{}, &uploadQuerier{})

	rr := postUpload(t, h, payload, checksum)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	var v interface{}
	if err := json.NewDecoder(rr.Body).Decode(&v); err != nil {
		t.Errorf("response body is not valid JSON: %v", err)
	}
}

// TestUploadHandler_201_TwoRequestsGetDistinctIDs verifies that two separate
// uploads each get a distinct UUID submission_id (Req 1.9 immutable unique ID).
func TestUploadHandler_201_TwoRequestsGetDistinctIDs(t *testing.T) {
	payload := minimalELF()
	checksum := hexSHA256(payload)
	h := newUploadHandler(&successRegistry{}, &uploadQuerier{})

	rr1 := postUpload(t, h, payload, checksum)
	rr2 := postUpload(t, h, payload, checksum)

	if rr1.Code != http.StatusCreated || rr2.Code != http.StatusCreated {
		t.Fatalf("expected both 201, got %d and %d", rr1.Code, rr2.Code)
	}

	var r1, r2 struct {
		SubmissionID string `json:"submission_id"`
	}
	json.NewDecoder(rr1.Body).Decode(&r1)
	json.NewDecoder(rr2.Body).Decode(&r2)

	if r1.SubmissionID == "" || r2.SubmissionID == "" {
		t.Fatal("both responses must contain a submission_id")
	}
	if r1.SubmissionID == r2.SubmissionID {
		t.Errorf("two uploads must produce distinct submission_ids, both got %q", r1.SubmissionID)
	}
}
