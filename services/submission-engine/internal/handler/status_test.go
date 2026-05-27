package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/submission-engine/internal/db"
	"github.com/iicpc/dbhp/submission-engine/internal/handler"
	"github.com/iicpc/dbhp/submission-engine/internal/middleware"
	"github.com/iicpc/dbhp/submission-engine/internal/store"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubGetter is a test double for handler.SubmissionGetter. It returns a
// pre-configured Submission or ErrNotFound, and optionally an error.
type stubGetter struct {
	submission store.Submission
	err        error
}

func (s *stubGetter) GetByID(_ context.Context, _ string) (store.Submission, error) {
	return s.submission, s.err
}

// stubQuerier is a minimal db.Querier that always returns ErrNoRows (unknown
// token), used to drive the auth middleware into the 401 path.
type stubQuerier struct{}

func (s *stubQuerier) LookupContestantByTokenHash(_ context.Context, _ []byte) (string, error) {
	// Return a non-nil error to simulate an invalid / unknown token.
	return "", store.ErrNotFound
}

func (s *stubQuerier) InsertSubmission(_ context.Context, _ db.InsertSubmissionParams) (string, error) {
	return "", nil
}

// stubValidQuerier always approves the token and returns a fixed contestant ID.
type stubValidQuerier struct {
	contestantID string
}

func (s *stubValidQuerier) LookupContestantByTokenHash(_ context.Context, _ []byte) (string, error) {
	return s.contestantID, nil
}

func (s *stubValidQuerier) InsertSubmission(_ context.Context, _ db.InsertSubmissionParams) (string, error) {
	return "", nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildHandler assembles:
//
//	Auth middleware (using querier) → Status handler (using getter)
//
// so that we can exercise both authentication and business logic in one
// HTTP call.
func buildHandler(querier db.Querier, getter handler.SubmissionGetter) http.Handler {
	logger := zap.NewNop()
	authMW := middleware.Auth(querier, logger)
	statusH := handler.Status(logger, getter)
	return authMW(statusH)
}

// doGet fires a GET request against the handler and returns the recorder.
func doGet(h http.Handler, path string, bearerToken string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	// Go 1.22 stdlib mux injects path values; when calling the handler directly
	// (without a mux) PathValue() returns "". We need to set the path value
	// manually so the handler under test can extract submission_id.
	req.SetPathValue("submission_id", path[len("/v1/submissions/"):len(path)-len("/status")])
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// decodeBody parses the recorder body as JSON into dest.
func decodeBody(t *testing.T, rr *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(dest); err != nil {
		t.Fatalf("failed to decode response body: %v\nbody: %s", err, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestStatus_200_AllFieldsPopulated verifies that a submission whose BuildLogURI
// is non-empty is returned as HTTP 200 with all three JSON fields present and
// build_log_url set to a non-null string.
func TestStatus_200_AllFieldsPopulated(t *testing.T) {
	submissionID := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	buildLogURL := "http://registry/logs/" + submissionID
	sub := store.Submission{
		ID:          submissionID,
		Status:      "BUILD_FAILED",
		BuildLogURI: buildLogURL,
	}
	h := buildHandler(&stubValidQuerier{contestantID: "contestant-1"}, &stubGetter{submission: sub})

	rr := doGet(h, "/v1/submissions/"+submissionID+"/status", "valid-token")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		SubmissionID string  `json:"submission_id"`
		Status       string  `json:"status"`
		BuildLogURL  *string `json:"build_log_url"`
	}
	decodeBody(t, rr, &resp)

	if resp.SubmissionID != submissionID {
		t.Errorf("submission_id: want %q, got %q", submissionID, resp.SubmissionID)
	}
	if resp.Status != "BUILD_FAILED" {
		t.Errorf("status: want %q, got %q", "BUILD_FAILED", resp.Status)
	}
	if resp.BuildLogURL == nil {
		t.Fatal("build_log_url: want non-null string, got null")
	}
	if *resp.BuildLogURL != buildLogURL {
		t.Errorf("build_log_url: want %q, got %q", buildLogURL, *resp.BuildLogURL)
	}
}

// TestStatus_200_NullBuildLogURL verifies that when the submission has no build
// log URI yet (e.g. status UPLOADED), build_log_url is marshalled as JSON null.
func TestStatus_200_NullBuildLogURL(t *testing.T) {
	submissionID := "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	sub := store.Submission{
		ID:          submissionID,
		Status:      "UPLOADED",
		BuildLogURI: "", // not yet set
	}
	h := buildHandler(&stubValidQuerier{contestantID: "contestant-2"}, &stubGetter{submission: sub})

	rr := doGet(h, "/v1/submissions/"+submissionID+"/status", "valid-token")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	// Decode into a map so we can distinguish null from absent.
	var raw map[string]any
	decodeBody(t, rr, &raw)

	if raw["submission_id"] != submissionID {
		t.Errorf("submission_id: want %q, got %v", submissionID, raw["submission_id"])
	}
	if raw["status"] != "UPLOADED" {
		t.Errorf("status: want UPLOADED, got %v", raw["status"])
	}
	// build_log_url key must exist and be null.
	urlVal, exists := raw["build_log_url"]
	if !exists {
		t.Fatal("build_log_url key missing from response")
	}
	if urlVal != nil {
		t.Errorf("build_log_url: want null, got %v", urlVal)
	}
}

// TestStatus_404_UnknownSubmissionID verifies that an unknown submission ID
// returns HTTP 404 with the error code SUBMISSION_NOT_FOUND.
func TestStatus_404_UnknownSubmissionID(t *testing.T) {
	h := buildHandler(
		&stubValidQuerier{contestantID: "contestant-3"},
		&stubGetter{err: store.ErrNotFound},
	)

	rr := doGet(h, "/v1/submissions/does-not-exist-uuid/status", "valid-token")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d; body: %s", rr.Code, rr.Body.String())
	}

	var errResp struct {
		Error string `json:"error"`
	}
	decodeBody(t, rr, &errResp)

	if errResp.Error != "SUBMISSION_NOT_FOUND" {
		t.Errorf("error: want SUBMISSION_NOT_FOUND, got %q", errResp.Error)
	}
}

// TestStatus_401_MissingToken verifies that a request without an Authorization
// header is rejected by the auth middleware with HTTP 401.
func TestStatus_401_MissingToken(t *testing.T) {
	sub := store.Submission{
		ID:     "any-id",
		Status: "UPLOADED",
	}
	h := buildHandler(&stubQuerier{}, &stubGetter{submission: sub})

	// No bearer token provided.
	rr := doGet(h, "/v1/submissions/any-id/status", "")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing token, got %d; body: %s", rr.Code, rr.Body.String())
	}

	var errResp struct {
		Error string `json:"error"`
	}
	decodeBody(t, rr, &errResp)

	if errResp.Error != "INVALID_TOKEN" {
		t.Errorf("error: want INVALID_TOKEN, got %q", errResp.Error)
	}
}

// TestStatus_401_InvalidToken verifies that a request with an unrecognised bearer
// token is rejected by the auth middleware with HTTP 401.
func TestStatus_401_InvalidToken(t *testing.T) {
	sub := store.Submission{
		ID:     "any-id",
		Status: "UPLOADED",
	}
	// stubQuerier always returns an error (unknown token).
	h := buildHandler(&stubQuerier{}, &stubGetter{submission: sub})

	rr := doGet(h, "/v1/submissions/any-id/status", "bad-token-xyz")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid token, got %d; body: %s", rr.Code, rr.Body.String())
	}

	var errResp struct {
		Error string `json:"error"`
	}
	decodeBody(t, rr, &errResp)

	if errResp.Error != "INVALID_TOKEN" {
		t.Errorf("error: want INVALID_TOKEN, got %q", errResp.Error)
	}
}
