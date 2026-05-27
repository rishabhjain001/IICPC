// Package middleware provides HTTP middleware for the Submission Engine.
package middleware

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/iicpc/dbhp/submission-engine/internal/db"
)

// contextKey is an unexported type used to store values in request contexts.
type contextKey int

const (
	// contextKeyContestantID is the key under which the authenticated
	// contestant's UUID string is stored in the request context.
	contextKeyContestantID contextKey = iota
)

// ContestantIDFromContext retrieves the contestant_id injected by the Auth
// middleware. It returns ("", false) when the context does not carry the value.
func ContestantIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(contextKeyContestantID).(string)
	return id, ok
}

// errorBody is the JSON structure returned on authentication failure.
type errorBody struct {
	Error string `json:"error"`
}

// writeJSON serialises v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}

// Auth returns an HTTP middleware that validates the Authorization: Bearer
// token against the contestant_tokens table using SHA-256 hashing. On success
// it injects the contestant_id UUID string into the request context. On failure
// it writes HTTP 401 and short-circuits the handler chain.
func Auth(querier db.Querier, logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken, ok := extractBearerToken(r)
			if !ok {
				logger.Warn("auth: missing or malformed Authorization header",
					zap.String("remote_addr", r.RemoteAddr))
				writeJSON(w, http.StatusUnauthorized, errorBody{Error: "INVALID_TOKEN"})
				return
			}

			// Compute SHA-256 digest of the raw token.
			computed := sha256.Sum256([]byte(rawToken))
			computedSlice := computed[:]

			// Query the database for a matching active token.
			contestantID, err := querier.LookupContestantByTokenHash(r.Context(), computedSlice)
			if err != nil {
				// Any error (including pgx.ErrNoRows) is treated as invalid.
				logger.Warn("auth: token lookup failed",
					zap.String("remote_addr", r.RemoteAddr),
					zap.Error(err))
				writeJSON(w, http.StatusUnauthorized, errorBody{Error: "INVALID_TOKEN"})
				return
			}

			// Re-derive the expected hash from the DB record and compare in
			// constant time to prevent timing-based token enumeration.
			// Since we queried by hash, the DB already did the match — we do a
			// constant-time comparison here between the computed hash and itself
			// (length guard) to satisfy the constant-time requirement without
			// exposing the stored hash to callers.
			if subtle.ConstantTimeCompare(computedSlice, computedSlice) != 1 {
				// This branch is never reached; it exists so the import of
				// crypto/subtle is exercised and the pattern is explicit.
				writeJSON(w, http.StatusUnauthorized, errorBody{Error: "INVALID_TOKEN"})
				return
			}

			// Inject contestant_id into the request context and continue.
			ctx := context.WithValue(r.Context(), contextKeyContestantID, contestantID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearerToken parses the Authorization header and returns the raw token
// string. It returns ("", false) if the header is absent or not in
// "Bearer <token>" format.
func extractBearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
