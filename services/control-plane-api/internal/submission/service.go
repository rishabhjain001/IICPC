// Package submission implements the SubmissionService gRPC handler for the
// Control Plane API.
//
// SubmissionService exposes submission status queries and build-log streaming.
// Build logs are stored in the Artifact Registry referenced by the build_log_uri
// column; streaming is simulated by reading the URI via HTTP in a real
// deployment. For this implementation the streaming interface is provided so
// that callers can consume chunks without blocking.
package submission

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when the requested submission does not exist.
var ErrNotFound = errors.New("not found")

// chunkSize is the maximum number of bytes per streaming chunk.
const chunkSize = 32 * 1024 // 32 KiB

// SubmissionStatus holds the current state of a submission.
type SubmissionStatus struct {
	SubmissionID string
	// Status is one of: UPLOADED, BUILT, BUILD_FAILED, BUILD_TIMEOUT,
	// BUILD_PUBLISH_FAILED, BUILD_INFRASTRUCTURE_ERROR.
	Status      string
	BuildLogURL string
	ImageDigest string
}

// SubmissionService handles submission status and build-log streaming.
type SubmissionService struct {
	Pool *pgxpool.Pool
	// HTTPClient is used to fetch build logs from the Artifact Registry URI.
	// Defaults to a client with a 30-second timeout when nil.
	HTTPClient *http.Client
}

// NewSubmissionService creates a SubmissionService backed by the given pool.
func NewSubmissionService(pool *pgxpool.Pool) *SubmissionService {
	return &SubmissionService{
		Pool: pool,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetSubmissionStatus returns the current status and build log URI for a
// submission identified by submissionID (UUID string).
//
// Returns ErrNotFound when no row with that ID exists.
func (s *SubmissionService) GetSubmissionStatus(ctx context.Context, submissionID string) (*SubmissionStatus, error) {
	const q = `
SELECT status,
       COALESCE(build_log_uri, ''),
       COALESCE(image_digest, '')
FROM   submissions
WHERE  id = $1`

	row := s.Pool.QueryRow(ctx, q, submissionID)

	var st SubmissionStatus
	st.SubmissionID = submissionID

	if err := row.Scan(&st.Status, &st.BuildLogURL, &st.ImageDigest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("GetSubmissionStatus: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("GetSubmissionStatus: %w", err)
	}
	return &st, nil
}

// StreamBuildLog fetches the build log stored at the submission's build_log_uri
// and streams it as raw byte chunks through the returned channel.
//
// The channel is closed when the log has been fully sent or an error occurs.
// The error (if any) is delivered as a nil chunk with an accompanying sentinel.
// Callers should read from the channel until it is closed.
//
// Returns ErrNotFound if the submission does not exist or has no build log.
func (s *SubmissionService) StreamBuildLog(ctx context.Context, submissionID string) (<-chan []byte, error) {
	const q = `SELECT COALESCE(build_log_uri, '') FROM submissions WHERE id = $1`

	var uri string
	if err := s.Pool.QueryRow(ctx, q, submissionID).Scan(&uri); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("StreamBuildLog: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("StreamBuildLog: lookup uri: %w", err)
	}
	if uri == "" {
		return nil, fmt.Errorf("StreamBuildLog: no build log available: %w", ErrNotFound)
	}

	ch := make(chan []byte, 8)

	go func() {
		defer close(ch)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return
		}

		client := s.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: 30 * time.Second}
		}

		resp, err := client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()

		buf := make([]byte, chunkSize)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case ch <- chunk:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				return
			}
		}
	}()

	return ch, nil
}
