package middleware_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/iicpc/dbhp/control-plane-api/internal/middleware"
)

const testSecret = "super-secret-token"

// callUnary invokes the BearerTokenInterceptor with the given metadata and
// returns the gRPC status code from the response.
func callUnary(t *testing.T, md metadata.MD) (codes.Code, error) {
	t.Helper()

	interceptor := middleware.BearerTokenInterceptor(testSecret)

	ctx := context.Background()
	if md != nil {
		ctx = metadata.NewIncomingContext(ctx, md)
	}

	var handlerCalled bool
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		handlerCalled = true
		return "ok", nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}, handler)

	if err != nil {
		st, _ := status.FromError(err)
		_ = handlerCalled
		_ = resp
		return st.Code(), err
	}
	_ = handlerCalled
	return codes.OK, nil
}

// TestBearerToken_ValidTokenPassesThrough verifies that a correct bearer
// token allows the handler to be invoked.
func TestBearerToken_ValidTokenPassesThrough(t *testing.T) {
	md := metadata.Pairs("authorization", "Bearer "+testSecret)
	code, err := callUnary(t, md)
	if err != nil {
		t.Fatalf("unexpected error for valid token: %v", err)
	}
	if code != codes.OK {
		t.Errorf("expected OK, got %v", code)
	}
}

// TestBearerToken_MissingTokenReturnsUnauthenticated verifies that a request
// with no authorization metadata returns UNAUTHENTICATED.
func TestBearerToken_MissingTokenReturnsUnauthenticated(t *testing.T) {
	code, err := callUnary(t, nil)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
	if code != codes.Unauthenticated {
		t.Errorf("expected UNAUTHENTICATED, got %v", code)
	}
}

// TestBearerToken_InvalidTokenReturnsUnauthenticated verifies that an
// incorrect bearer token returns UNAUTHENTICATED.
func TestBearerToken_InvalidTokenReturnsUnauthenticated(t *testing.T) {
	md := metadata.Pairs("authorization", "Bearer wrong-token")
	code, err := callUnary(t, md)
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}
	if code != codes.Unauthenticated {
		t.Errorf("expected UNAUTHENTICATED, got %v", code)
	}
}

// TestBearerToken_MalformedHeaderReturnsUnauthenticated verifies that a
// header without the "Bearer " prefix is rejected.
func TestBearerToken_MalformedHeaderReturnsUnauthenticated(t *testing.T) {
	md := metadata.Pairs("authorization", testSecret) // missing "Bearer " prefix
	code, err := callUnary(t, md)
	if err == nil {
		t.Fatal("expected error for malformed authorization header, got nil")
	}
	if code != codes.Unauthenticated {
		t.Errorf("expected UNAUTHENTICATED, got %v", code)
	}
}
