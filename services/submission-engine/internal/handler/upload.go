// Package handler implements the HTTP handlers for the Submission Engine.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/google/uuid"
	"github.com/iicpc/dbhp/submission-engine/internal/db"
	"github.com/iicpc/dbhp/submission-engine/internal/middleware"
	"github.com/iicpc/dbhp/submission-engine/internal/registry"
	"github.com/iicpc/dbhp/submission-engine/internal/validator"
)

// RateLimiter is the interface the Upload handler uses to enforce the
// per-contestant rolling-window upload limit (Requirement 1.6 / 1.7).
//
// Allow checks whether the contestant is within the rate limit and, if so,
// atomically records the upload.  It returns:
//   - (true, 0, nil)           — upload allowed (and recorded).
//   - (false, retryAfter, nil) — rate limited; retryAfter is seconds until the
//     next upload is permitted.
//   - (false, 0, err)          — Redis error.
type RateLimiter interface {
	Allow(ctx context.Context, contestantID string) (allowed bool, retryAfter int64, err error)
}

// uploadResponse is the JSON body returned on a successful upload (HTTP 201).
type uploadResponse struct {
	SubmissionID string `json:"submission_id"`
	Status       string `json:"status"`
}

// errorResponse is the JSON body returned on handler-level errors.
type errorResponse struct {
	Error      string `json:"error"`
	MaxBytes   int64  `json:"max_bytes,omitempty"`
	RetryAfter int64  `json:"retry_after,omitempty"`
}

// writeJSON serialises v as JSON and writes it with the given HTTP status code.
func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}

// Upload handles POST /v1/submissions.
//
// Pipeline:
//  1. Auth middleware already validated the bearer token and injected contestant_id.
//  2. Rate-limit check (Requirement 1.6 / 1.7) — HTTP 429 with Retry-After on excess.
//  3. Parse the multipart form to locate the "artifact" field.
//  4. Read the X-Checksum-SHA256 header.
//  5. Stream the artifact through ValidateArtifact:
//     - size cap  → 413
//     - bad format / missing manifest → 415
//     - checksum mismatch → 422 (partial data deleted in memory; no disk cleanup needed)
//  6. Push artifact to the Artifact Registry (Requirement 1.8):
//     - registry unavailable → 503 (do NOT assign a Submission ID per Req 1.10)
//  7. Assign a UUID v4 Submission ID, insert a row in the submissions table,
//     and return HTTP 201 {"submission_id": "...", "status": "UPLOADED"} (Req 1.9).
func Upload(logger *zap.Logger, limiter RateLimiter, reg registry.Client, queries db.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Defensive: Auth middleware must have set the contestant_id.
		contestantID, ok := middleware.ContestantIDFromContext(r.Context())
		if !ok {
			logger.Error("upload: contestant_id missing from context — Auth middleware misconfigured")
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "INVALID_TOKEN"})
			return
		}

		// ---- Rate limit check (Requirement 1.6 / 1.7) ----
		allowed, retryAfter, rlErr := limiter.Allow(r.Context(), contestantID)
		if rlErr != nil {
			logger.Error("upload: rate limiter error",
				zap.String("contestant_id", contestantID),
				zap.Error(rlErr),
			)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "INTERNAL_ERROR"})
			return
		}
		if !allowed {
			logger.Warn("upload: rate limit exceeded",
				zap.String("contestant_id", contestantID),
				zap.Int64("retry_after_seconds", retryAfter),
			)
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			writeJSON(w, http.StatusTooManyRequests, errorResponse{
				Error:      "RATE_LIMITED",
				RetryAfter: retryAfter,
			})
			return
		}

		// A correlation ID for logging during this request (before a real ID is assigned).
		requestID := uuid.New().String()

		logger.Info("upload: artifact received",
			zap.String("request_id", requestID),
			zap.String("contestant_id", contestantID),
			zap.String("remote_addr", r.RemoteAddr),
		)

		// Parse the multipart form.
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			logger.Warn("upload: failed to parse multipart form",
				zap.String("request_id", requestID),
				zap.String("contestant_id", contestantID),
				zap.Error(err),
			)
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "BAD_REQUEST"})
			return
		}

		if r.MultipartForm == nil || r.MultipartForm.File["artifact"] == nil {
			logger.Warn("upload: missing artifact field in multipart form",
				zap.String("request_id", requestID),
				zap.String("contestant_id", contestantID),
			)
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "MISSING_ARTIFACT"})
			return
		}

		fileHeaders := r.MultipartForm.File["artifact"]
		if len(fileHeaders) == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "MISSING_ARTIFACT"})
			return
		}
		fileHeader := fileHeaders[0]
		artifactFile, err := fileHeader.Open()
		if err != nil {
			logger.Error("upload: failed to open artifact file part",
				zap.String("request_id", requestID),
				zap.Error(err),
			)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "INTERNAL_ERROR"})
			return
		}
		defer artifactFile.Close()

		// Read the expected checksum from the request header.
		expectedChecksum := r.Header.Get("X-Checksum-SHA256")

		// ---- Artifact validation ----
		result, artifactData, validErr := validator.ValidateArtifact(artifactFile, expectedChecksum)
		if validErr != nil {
			switch {
			case errors.Is(validErr, validator.ErrTooLarge):
				logger.Warn("upload: artifact too large",
					zap.String("request_id", requestID),
					zap.String("contestant_id", contestantID),
				)
				writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{
					Error:    "ARTIFACT_TOO_LARGE",
					MaxBytes: validator.MaxArtifactBytes,
				})

			case errors.Is(validErr, validator.ErrBadFormat):
				logger.Warn("upload: unsupported artifact format",
					zap.String("request_id", requestID),
					zap.String("contestant_id", contestantID),
				)
				writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{Error: "UNSUPPORTED_FORMAT"})

			case errors.Is(validErr, validator.ErrBadChecksum):
				// Per Requirement 1.5: delete any partial data.
				// Artifact is buffered in memory only; no on-disk cleanup needed.
				logger.Warn("upload: checksum mismatch",
					zap.String("request_id", requestID),
					zap.String("contestant_id", contestantID),
				)
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "CHECKSUM_MISMATCH"})

			default:
				logger.Error("upload: unexpected validation error",
					zap.String("request_id", requestID),
					zap.Error(validErr),
				)
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "INTERNAL_ERROR"})
			}
			return
		}

		logger.Info("upload: artifact validated",
			zap.String("request_id", requestID),
			zap.String("contestant_id", contestantID),
			zap.String("artifact_type", string(result.Type)),
			zap.Int64("artifact_size", result.Size),
		)

		// ---- Artifact Registry push (Requirement 1.8) ----
		// Per Requirement 1.10: if the registry is unavailable, return 503 and
		// do NOT assign a Submission ID.
		artifactURI, pushErr := reg.PushArtifact(
			r.Context(),
			requestID, // temporary tag; overwritten after we get the real UUID below
			string(result.Type),
			bytes.NewReader(artifactData),
			result.Size,
		)
		if pushErr != nil {
			if errors.Is(pushErr, registry.ErrRegistryUnavailable) {
				logger.Warn("upload: artifact registry unavailable",
					zap.String("request_id", requestID),
					zap.String("contestant_id", contestantID),
					zap.Error(pushErr),
				)
				writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "REGISTRY_UNAVAILABLE"})
				return
			}
			logger.Error("upload: registry push error",
				zap.String("request_id", requestID),
				zap.String("contestant_id", contestantID),
				zap.Error(pushErr),
			)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "INTERNAL_ERROR"})
			return
		}

		// ---- Assign UUID Submission ID (Requirement 1.9) ----
		// Only assigned AFTER a successful registry push.
		submissionID := uuid.New().String()

		// Compute the raw SHA-256 bytes for DB storage.
		checksumBytes := sha256.Sum256(artifactData)

		// ---- Persist to submissions table ----
		_, dbErr := queries.InsertSubmission(r.Context(), db.InsertSubmissionParams{
			ID:             submissionID,
			ContestantID:   contestantID,
			ArtifactType:   string(result.Type),
			ArtifactSize:   result.Size,
			ChecksumSHA256: checksumBytes[:],
			Status:         "UPLOADED",
			ArtifactURI:    artifactURI,
		})
		if dbErr != nil {
			logger.Error("upload: failed to insert submission row",
				zap.String("submission_id", submissionID),
				zap.String("contestant_id", contestantID),
				zap.Error(dbErr),
			)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "INTERNAL_ERROR"})
			return
		}

		logger.Info("upload: submission created",
			zap.String("submission_id", submissionID),
			zap.String("contestant_id", contestantID),
			zap.String("artifact_uri", artifactURI),
		)

		// HTTP 201 with submission_id (Requirement 1.9).
		writeJSON(w, http.StatusCreated, uploadResponse{
			SubmissionID: submissionID,
			Status:       "UPLOADED",
		})
	}
}
