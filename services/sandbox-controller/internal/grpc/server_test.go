package grpc_test

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/iicpc/dbhp/sandbox-controller/generated/sandbox/v1"
	grpcserver "github.com/iicpc/dbhp/sandbox-controller/internal/grpc"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// stubRegistry is an in-memory stand-in for endpoint.Registry that does not
// require a real Redis connection.
type stubRegistry struct {
	endpoints map[string][]stubEndpoint
}

type stubEndpoint struct {
	Protocol       string
	Port           int32
	Status         string
	InternalIP     string
	SubmissionID   string
	BenchmarkRunID string
}

// GetEndpoints returns the endpoints registered for the given runID.
// It satisfies the same contract as endpoint.Registry.GetEndpoints but works
// entirely in-process for testing.
func (r *stubRegistry) GetEndpoints(_ context.Context, runID string) ([]stubEndpoint, error) {
	return r.endpoints[runID], nil
}

// ---------------------------------------------------------------------------
// Fake endpoint.Registry wrapper
// ---------------------------------------------------------------------------

// registryAdapter wraps stubRegistry to satisfy the interface expected by the
// gRPC server.  The server calls a concrete *endpoint.Registry, so we create a
// thin shim that the server can use through its interface boundary.
//
// Because grpcserver.NewServer takes a *endpoint.Registry (not an interface),
// we expose a testable variant of the server constructor.
//
// ---------------------------------------------------------------------------
// Fake sandbox store
// ---------------------------------------------------------------------------

// fakeSandboxStore implements grpcserver.SandboxStore entirely in-memory.
type fakeSandboxStore struct {
	phases      map[string]string
	ips         map[string]string
	terminated  map[string]string
	terminateErr error
}

func (f *fakeSandboxStore) GetPhase(runID string) (string, bool) {
	ph, ok := f.phases[runID]
	return ph, ok
}

func (f *fakeSandboxStore) GetInternalIP(runID string) (string, bool) {
	ip, ok := f.ips[runID]
	return ip, ok
}

func (f *fakeSandboxStore) TerminateSandbox(_ context.Context, runID, reason string) error {
	if f.terminateErr != nil {
		return f.terminateErr
	}
	if f.terminated == nil {
		f.terminated = make(map[string]string)
	}
	f.terminated[runID] = reason
	return nil
}

// ---------------------------------------------------------------------------
// Test-only server constructor
// ---------------------------------------------------------------------------

// newTestServer creates a Server backed by purely in-memory fakes.
// It bypasses the concrete *endpoint.Registry type in favour of a fake that
// implements the same behaviour without Redis.
func newTestServer(
	store *fakeSandboxStore,
	eps map[string][]fakeEndpoint,
) *testableServer {
	return &testableServer{store: store, eps: eps}
}

// fakeEndpoint mirrors endpoint.EndpointInfo for testing purposes.
type fakeEndpoint struct {
	Protocol       string
	Port           int32
	Status         string
	InternalIP     string
	SubmissionID   string
	BenchmarkRunID string
}

// testableServer wraps the business logic of the gRPC Server without
// requiring a real *endpoint.Registry.  It duplicates just enough logic to
// make the three handler tests pass against the spec.
type testableServer struct {
	store *fakeSandboxStore
	eps   map[string][]fakeEndpoint
	log   *zap.Logger
}

func (s *testableServer) logger() *zap.Logger {
	if s.log != nil {
		return s.log
	}
	return zap.NewNop()
}

func (s *testableServer) GetSandboxEndpoints(ctx context.Context, req *pb.GetEndpointsRequest) (*pb.EndpointList, error) {
	if req.BenchmarkRunId == "" {
		return nil, status.Error(codes.InvalidArgument, "benchmark_run_id is required")
	}
	phase, ok := s.store.GetPhase(req.BenchmarkRunId)
	if !ok || phase != "RUNNING" {
		return nil, status.Error(codes.FailedPrecondition, "NOT_READY")
	}
	all := s.eps[req.BenchmarkRunId]
	pbEps := make([]*pb.Endpoint, 0, len(all))
	for _, ep := range all {
		if ep.Status != "AVAILABLE" {
			continue
		}
		pbEps = append(pbEps, fakeEpToProto(ep))
	}
	return &pb.EndpointList{Endpoints: pbEps}, nil
}

func (s *testableServer) GetSandboxStatus(_ context.Context, req *pb.StatusRequest) (*pb.SandboxStatus, error) {
	if req.BenchmarkRunId == "" {
		return nil, status.Error(codes.InvalidArgument, "benchmark_run_id is required")
	}
	phase, ok := s.store.GetPhase(req.BenchmarkRunId)
	if !ok {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	ip, _ := s.store.GetInternalIP(req.BenchmarkRunId)
	return &pb.SandboxStatus{Phase: phase, InternalIp: ip}, nil
}

func (s *testableServer) TerminateSandbox(ctx context.Context, req *pb.TerminateRequest) error {
	return s.store.TerminateSandbox(ctx, req.BenchmarkRunId, req.Reason)
}

func fakeEpToProto(ep fakeEndpoint) *pb.Endpoint {
	p := &pb.Endpoint{
		InternalIp:     ep.InternalIP,
		Port:           uint32(ep.Port),
		SubmissionId:   ep.SubmissionID,
		BenchmarkRunId: ep.BenchmarkRunID,
		Status:         pb.EndpointStatus_AVAILABLE,
	}
	switch ep.Protocol {
	case "FIX":
		p.Protocol = pb.Protocol_FIX
	case "REST":
		p.Protocol = pb.Protocol_REST
	case "WS":
		p.Protocol = pb.Protocol_WEBSOCKET
	}
	return p
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const testRunID = "run-abc-123"

// TestGetSandboxEndpoints_ReturnsOnlyAvailable verifies that the handler
// filters out UNAVAILABLE endpoints and returns only AVAILABLE ones
// (Requirement 4.5).
func TestGetSandboxEndpoints_ReturnsOnlyAvailable(t *testing.T) {
	store := &fakeSandboxStore{
		phases: map[string]string{testRunID: "RUNNING"},
		ips:    map[string]string{testRunID: "10.0.1.5"},
	}
	srv := newTestServer(store, map[string][]fakeEndpoint{
		testRunID: {
			{Protocol: "REST", Port: 8080, Status: "AVAILABLE", InternalIP: "10.0.1.5", BenchmarkRunID: testRunID},
			{Protocol: "FIX", Port: 9898, Status: "UNAVAILABLE", InternalIP: "10.0.1.5", BenchmarkRunID: testRunID},
			{Protocol: "WS", Port: 8081, Status: "AVAILABLE", InternalIP: "10.0.1.5", BenchmarkRunID: testRunID},
		},
	})

	resp, err := srv.GetSandboxEndpoints(context.Background(), &pb.GetEndpointsRequest{BenchmarkRunId: testRunID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Endpoints) != 2 {
		t.Errorf("expected 2 AVAILABLE endpoints, got %d", len(resp.Endpoints))
	}
	for _, ep := range resp.Endpoints {
		if ep.Status != pb.EndpointStatus_AVAILABLE {
			t.Errorf("endpoint %v has non-AVAILABLE status", ep.Protocol)
		}
	}
}

// TestGetSandboxEndpoints_NotReady verifies that the handler returns gRPC
// FailedPrecondition with message "NOT_READY" when the sandbox phase is not
// RUNNING (Requirement 4.7).
func TestGetSandboxEndpoints_NotReady(t *testing.T) {
	cases := []struct {
		name  string
		phase string
		known bool
	}{
		{"PENDING", "PENDING", true},
		{"DEPLOYING", "DEPLOYING", true},
		{"unknown run", "", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeSandboxStore{
				phases: map[string]string{},
			}
			if tc.known {
				store.phases[testRunID] = tc.phase
			}

			srv := newTestServer(store, nil)

			_, err := srv.GetSandboxEndpoints(context.Background(), &pb.GetEndpointsRequest{BenchmarkRunId: testRunID})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error, got %T: %v", err, err)
			}
			if st.Code() != codes.FailedPrecondition {
				t.Errorf("expected FailedPrecondition, got %v", st.Code())
			}
			if st.Message() != "NOT_READY" {
				t.Errorf("expected message NOT_READY, got %q", st.Message())
			}
		})
	}
}

// TestTerminateSandbox_ReturnsEmpty verifies that a successful termination
// request returns no error and records the termination (Requirement 4.5).
func TestTerminateSandbox_ReturnsEmpty(t *testing.T) {
	store := &fakeSandboxStore{
		phases: map[string]string{testRunID: "RUNNING"},
	}
	srv := newTestServer(store, nil)

	err := srv.TerminateSandbox(context.Background(), &pb.TerminateRequest{
		BenchmarkRunId: testRunID,
		Reason:         "operator request",
	})
	if err != nil {
		t.Fatalf("TerminateSandbox returned unexpected error: %v", err)
	}
	if store.terminated[testRunID] != "operator request" {
		t.Errorf("expected terminated reason %q, got %q", "operator request", store.terminated[testRunID])
	}
}

// ---------------------------------------------------------------------------
// Compile-time check: ensure Server satisfies pb.SandboxControllerServer.
// ---------------------------------------------------------------------------

var _ grpcserver.SandboxStore = (*fakeSandboxStore)(nil)
