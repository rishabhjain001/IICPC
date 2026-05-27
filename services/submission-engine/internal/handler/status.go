package handler

import (
	"context"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/submission-engine/internal/store"
)

// SubmissionGetter is the interface the Status handler uses to retrieve a
// submission by its ID.  It is satisfied by *store.SubmissionStore and can be
// mocked in tests.
type SubmissionGetter interface {
	GetByID(ctx context.Context, id string) (store.Submission, error)
}

// statusResponse is the JSON body returned by GET
// /v1/submissions/{submission_id}/status on success.
//
// BuildLogURL uses a pointer so that it marshals as JSON null when no log URI
// has been set yet (rather than an empty string).
type statusResponse struct {
	SubmissionID string  `json:"submission_id"`
	Status       string  `json:"status"`
	BuildLogURL  *string `json:"build_log_url"`
}

// Status handles GET /v1/submissions/{submission_id}/status.
//
// The handler:
//  1. Extracts the submission_id path parameter (Go 1.22 stdlib routing).
//  2. Looks up the submission via the store.
//  3. Returns HTTP 404 {"error":"SUBMISSION_NOT_FOUND"} when no row matches.
//  4. Returns HTTP 200 with submission_id, status, and build_log_url on success.
//     build_log_url is null in JSON when not yet set.
//
// Authentication is enforced by the global Auth middleware; the handler itself
// only needs to serve the authorised request.
func Status(logger *zap.Logger, getter SubmissionGetter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		submissionID := r.PathValue("submission_id")
		if submissionID == "" {
			// Should not happen with Go 1.22 pattern routing, but guard anyway.
			logger.Warn("status: empty submission_id path value")
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "BAD_REQUEST"})
			return
		}

		sub, err := getter.GetByID(r.Context(), submissionID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				logger.Info("status: submission not found",
					zap.String("submission_id", submissionID),
				)
				writeJSON(w, http.StatusNotFound, errorResponse{Error: "SUBMISSION_NOT_FOUND"})
				return
			}
			logger.Error("status: store error",
				zap.String("submission_id", submissionID),
				zap.Error(err),
			)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "INTERNAL_ERROR"})
			return
		}

		// Build log URL is nullable: only populate it when a URI has been stored.
		var buildLogURL *string
		if sub.BuildLogURI != "" {
			buildLogURL = &sub.BuildLogURI
		}

		writeJSON(w, http.StatusOK, statusResponse{
			SubmissionID: sub.ID,
			Status:       sub.Status,
			BuildLogURL:  buildLogURL,
		})
	}
}
