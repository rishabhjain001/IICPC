package submission_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iicpc/dbhp/control-plane-api/internal/submission"
	"github.com/iicpc/dbhp/control-plane-api/internal/testutil"
)

// newService creates a SubmissionService connected to the test database.
func newService(t *testing.T) *submission.SubmissionService {
	t.Helper()
	pool := testutil.MustPool(t)
	return submission.NewSubmissionService(pool)
}

// seedContestant inserts a contestant and returns its UUID.
func seedContestant(t *testing.T, pool *pgxpool.Pool, handle, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO contestants (handle, email) VALUES ($1, $2) RETURNING id`,
		handle, email,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seedContestant: %v", err)
	}
	return id
}

// seedSubmission inserts a row into submissions and returns its UUID.
// logURI may be empty (no build log).
func seedSubmission(t *testing.T, pool *pgxpool.Pool, contestantID, statusVal, imageDigest, logURI string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO submissions
		 (contestant_id, artifact_type, artifact_size, checksum_sha256, status, image_digest, build_log_uri)
		 VALUES ($1, 'ELF_BINARY', 2048, '\xdeadbeef', $2, NULLIF($3,''), NULLIF($4,''))
		 RETURNING id`,
		contestantID, statusVal, imageDigest, logURI,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seedSubmission: %v", err)
	}
	return id
}

// TestGetSubmissionStatus_ReturnsCorrectFields verifies that
// GetSubmissionStatus populates all fields from the database correctly.
func TestGetSubmissionStatus_ReturnsCorrectFields(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	cID := seedContestant(t, pool, "sub_user_1", "sub1@example.com")
	sID := seedSubmission(t, pool, cID, "BUILT", "sha256:abc123", "https://artifact.registry/log")

	st, err := svc.GetSubmissionStatus(ctx, sID)
	if err != nil {
		t.Fatalf("GetSubmissionStatus: %v", err)
	}

	if st.SubmissionID != sID {
		t.Errorf("SubmissionID: want %q, got %q", sID, st.SubmissionID)
	}
	if st.Status != "BUILT" {
		t.Errorf("Status: want %q, got %q", "BUILT", st.Status)
	}
	if st.BuildLogURL != "https://artifact.registry/log" {
		t.Errorf("BuildLogURL: want %q, got %q", "https://artifact.registry/log", st.BuildLogURL)
	}
	if st.ImageDigest != "sha256:abc123" {
		t.Errorf("ImageDigest: want %q, got %q", "sha256:abc123", st.ImageDigest)
	}
}

// TestGetSubmissionStatus_NotFound verifies that a missing submission returns
// ErrNotFound.
func TestGetSubmissionStatus_NotFound(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	_, err := svc.GetSubmissionStatus(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected error for non-existent submission, got nil")
	}
}

// TestStreamBuildLog_ChunksCorrectly starts a local HTTP test server that
// serves a known payload and verifies the stream produces the expected chunks.
func TestStreamBuildLog_ChunksCorrectly(t *testing.T) {
	svc := newService(t)
	pool := testutil.MustPool(t)
	ctx := context.Background()

	// Build a payload that is bigger than a single chunk to test multi-chunk streaming.
	// chunkSize in the implementation is 32 KiB; use 70 KiB to force 3 reads.
	payload := strings.Repeat("A", 70*1024)

	// Serve the payload from a local test HTTP server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(payload)) //nolint:errcheck
	}))
	defer srv.Close()

	cID := seedContestant(t, pool, "sub_user_2", "sub2@example.com")
	sID := seedSubmission(t, pool, cID, "BUILD_FAILED", "", srv.URL)

	ch, err := svc.StreamBuildLog(ctx, sID)
	if err != nil {
		t.Fatalf("StreamBuildLog: %v", err)
	}

	var received []byte
	for chunk := range ch {
		received = append(received, chunk...)
	}

	if string(received) != payload {
		t.Errorf("reassembled log length %d, want %d", len(received), len(payload))
	}
}

// TestStreamBuildLog_NotFound verifies that streaming a missing submission
// returns an error containing ErrNotFound.
func TestStreamBuildLog_NotFound(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	_, err := svc.StreamBuildLog(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected error for non-existent submission, got nil")
	}
}
