// Package sandboxv1 provides hand-written Go types that mirror what protoc
// would generate from proto/dbhp/sandbox/v1/sandbox.proto.  This package is
// used when protoc is not available in the build environment.
//
// The package follows the same conventions as protoc-generated code:
//   - Message structs have exported fields with `json:` tags matching proto
//     field names.
//   - Enum values are typed int32 constants.
//   - The UnimplementedSandboxControllerServer base struct satisfies the
//     SandboxControllerServer interface so that partial implementations
//     compile without error.
package sandboxv1

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ---------------------------------------------------------------------------
// Enumerations
// ---------------------------------------------------------------------------

// Protocol identifies the application-layer protocol of a sandbox endpoint.
type Protocol int32

const (
	Protocol_FIX       Protocol = 0
	Protocol_REST      Protocol = 1
	Protocol_WEBSOCKET Protocol = 2
)

// EndpointStatus describes the availability of a single endpoint.
type EndpointStatus int32

const (
	EndpointStatus_AVAILABLE   EndpointStatus = 0
	EndpointStatus_UNAVAILABLE EndpointStatus = 1
)

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

// GetEndpointsRequest is the request message for GetSandboxEndpoints.
type GetEndpointsRequest struct {
	BenchmarkRunId string `json:"benchmark_run_id,omitempty"`
}

// Endpoint carries metadata for a single protocol endpoint.
type Endpoint struct {
	InternalIp     string         `json:"internal_ip,omitempty"`
	Port           uint32         `json:"port,omitempty"`
	Protocol       Protocol       `json:"protocol,omitempty"`
	SubmissionId   string         `json:"submission_id,omitempty"`
	BenchmarkRunId string         `json:"benchmark_run_id,omitempty"`
	Status         EndpointStatus `json:"status,omitempty"`
}

// EndpointList is the response message for GetSandboxEndpoints.
type EndpointList struct {
	Endpoints      []*Endpoint `json:"endpoints,omitempty"`
	NotReadyReason string      `json:"not_ready_reason,omitempty"`
}

// StatusRequest is the request message for GetSandboxStatus.
type StatusRequest struct {
	BenchmarkRunId string `json:"benchmark_run_id,omitempty"`
}

// SandboxStatus is the response message for GetSandboxStatus.
type SandboxStatus struct {
	Phase             string `json:"phase,omitempty"`
	InternalIp        string `json:"internal_ip,omitempty"`
	TerminationReason string `json:"termination_reason,omitempty"`
	ExitCode          int32  `json:"exit_code,omitempty"`
}

// TerminateRequest is the request message for TerminateSandbox.
type TerminateRequest struct {
	BenchmarkRunId string `json:"benchmark_run_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// ---------------------------------------------------------------------------
// gRPC service interface
// ---------------------------------------------------------------------------

// SandboxControllerClient is the client API for SandboxController service.
type SandboxControllerClient interface {
	GetSandboxEndpoints(ctx context.Context, in *GetEndpointsRequest, opts ...grpc.CallOption) (*EndpointList, error)
	GetSandboxStatus(ctx context.Context, in *StatusRequest, opts ...grpc.CallOption) (*SandboxStatus, error)
	TerminateSandbox(ctx context.Context, in *TerminateRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
}

// SandboxControllerServer is the server API for SandboxController service.
// All implementations must embed UnimplementedSandboxControllerServer for
// forward compatibility.
type SandboxControllerServer interface {
	GetSandboxEndpoints(context.Context, *GetEndpointsRequest) (*EndpointList, error)
	GetSandboxStatus(context.Context, *StatusRequest) (*SandboxStatus, error)
	TerminateSandbox(context.Context, *TerminateRequest) (*emptypb.Empty, error)
	mustEmbedUnimplementedSandboxControllerServer()
}

// UnimplementedSandboxControllerServer must be embedded to satisfy the
// SandboxControllerServer interface.  All methods return
// codes.Unimplemented by default.
type UnimplementedSandboxControllerServer struct{}

func (UnimplementedSandboxControllerServer) GetSandboxEndpoints(_ context.Context, _ *GetEndpointsRequest) (*EndpointList, error) {
	return nil, nil
}
func (UnimplementedSandboxControllerServer) GetSandboxStatus(_ context.Context, _ *StatusRequest) (*SandboxStatus, error) {
	return nil, nil
}
func (UnimplementedSandboxControllerServer) TerminateSandbox(_ context.Context, _ *TerminateRequest) (*emptypb.Empty, error) {
	return nil, nil
}
func (UnimplementedSandboxControllerServer) mustEmbedUnimplementedSandboxControllerServer() {}
