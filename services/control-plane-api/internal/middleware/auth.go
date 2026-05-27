// Package middleware provides gRPC server interceptors for the Control Plane
// API service.
package middleware

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// bearerPrefix is the expected prefix of the Authorization metadata value.
const bearerPrefix = "Bearer "

// extractBearerToken extracts the raw bearer token from gRPC metadata.
// Returns an empty string if the header is absent or malformed.
func extractBearerToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}

	authHeader := vals[0]
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return ""
	}

	return strings.TrimPrefix(authHeader, bearerPrefix)
}

// BearerTokenInterceptor returns a gRPC UnaryServerInterceptor that validates
// the shared secret bearer token on all inbound calls.
//
// The token is read from the "authorization" gRPC metadata header with value
// "Bearer <token>". Returns UNAUTHENTICATED if the header is absent or the
// token does not match sharedSecret.
func BearerTokenInterceptor(sharedSecret string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		token := extractBearerToken(ctx)
		if token == "" || token != sharedSecret {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid bearer token")
		}
		return handler(ctx, req)
	}
}

// BearerTokenStreamInterceptor returns a gRPC StreamServerInterceptor that
// validates the shared secret bearer token on all inbound streaming calls.
//
// Returns UNAUTHENTICATED if the header is absent or the token does not match.
func BearerTokenStreamInterceptor(sharedSecret string) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		token := extractBearerToken(ss.Context())
		if token == "" || token != sharedSecret {
			return status.Error(codes.Unauthenticated, "missing or invalid bearer token")
		}
		return handler(srv, ss)
	}
}
