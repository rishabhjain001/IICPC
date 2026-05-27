// Package grpc implements the SandboxController gRPC service that is
// consumed by the Bot Fleet Manager and Control Plane.
//
// Requirements: 4.5, 4.6, 4.7
package grpc

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/iicpc/dbhp/sandbox-controller/internal/endpoint"
	pb "github.com/iicpc/dbhp/sandbox-controller/generated/sandbox/v1"
)

// SandboxStore is the interface for looking up the current phase and internal
// IP of a Sandbox by its BenchmarkRunID.  The Reconciler / watcher writes
// this information; the gRPC server reads it.
type SandboxStore interface {
	// GetPhase returns the current sandbox phase string (e.g. "RUNNING",
	// "PENDING") for the given Benchmark Run ID.  Returns ("", false) when
	// the run is not known.
	GetPhase(benchmarkRunID string) (phase string, ok bool)

	// GetInternalIP returns the internal cluster IP assigned to the sandbox
	// pod.  Returns ("", false) when the run is not known or the pod has
	// not yet been assigned an IP.
	GetInternalIP(benchmarkRunID string) (ip string, ok bool)

	// TerminateSandbox signals the controller to gracefully stop the sandbox
	// associated with the given Benchmark Run ID.
	TerminateSandbox(ctx context.Context, benchmarkRunID, reason string) error
}

// Server implements the SandboxController gRPC service.
type Server struct {
	pb.UnimplementedSandboxControllerServer
	endpointRegistry *endpoint.Registry
	sandboxStore     SandboxStore
	log              *zap.Logger
}

// NewServer creates a new Server wired to the provided dependencies.
func NewServer(reg *endpoint.Registry, store SandboxStore, log *zap.Logger) *Server {
	return &Server{
		endpointRegistry: reg,
		sandboxStore:     store,
		log:              log,
	}
}

// GetSandboxEndpoints returns only AVAILABLE endpoints for the requested
// Benchmark Run.  Returns gRPC FailedPrecondition ("NOT_READY") when the
// sandbox has not yet reached RUNNING state (Requirement 4.7).
//
// Filters out any endpoint whose Status != "AVAILABLE" before returning
// (Requirement 4.5).
func (s *Server) GetSandboxEndpoints(ctx context.Context, req *pb.GetEndpointsRequest) (*pb.EndpointList, error) {
	if req.BenchmarkRunId == "" {
		return nil, status.Error(codes.InvalidArgument, "benchmark_run_id is required")
	}

	phase, ok := s.sandboxStore.GetPhase(req.BenchmarkRunId)
	if !ok {
		// The run is completely unknown — treat as not ready.
		return nil, status.Error(codes.FailedPrecondition, "NOT_READY")
	}

	if phase != "RUNNING" {
		s.log.Debug("GetSandboxEndpoints: sandbox not running",
			zap.String("benchmarkRunId", req.BenchmarkRunId),
			zap.String("phase", phase),
		)
		return nil, status.Error(codes.FailedPrecondition, "NOT_READY")
	}

	endpoints, err := s.endpointRegistry.GetEndpoints(ctx, req.BenchmarkRunId)
	if err != nil {
		s.log.Error("GetSandboxEndpoints: registry lookup failed",
			zap.String("benchmarkRunId", req.BenchmarkRunId),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, fmt.Sprintf("endpoint registry: %v", err))
	}

	// Filter to AVAILABLE endpoints only (Requirement 4.5).
	pbEndpoints := make([]*pb.Endpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep.Status != "AVAILABLE" {
			continue
		}
		pbEndpoints = append(pbEndpoints, endpointInfoToProto(ep))
	}

	return &pb.EndpointList{Endpoints: pbEndpoints}, nil
}

// GetSandboxStatus returns the current phase and internal IP for the sandbox
// associated with the given Benchmark Run ID (Requirement 4.5).
func (s *Server) GetSandboxStatus(ctx context.Context, req *pb.StatusRequest) (*pb.SandboxStatus, error) {
	if req.BenchmarkRunId == "" {
		return nil, status.Error(codes.InvalidArgument, "benchmark_run_id is required")
	}

	phase, ok := s.sandboxStore.GetPhase(req.BenchmarkRunId)
	if !ok {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}

	ip, _ := s.sandboxStore.GetInternalIP(req.BenchmarkRunId)

	return &pb.SandboxStatus{
		Phase:      phase,
		InternalIp: ip,
	}, nil
}

// TerminateSandbox triggers a graceful shutdown of the sandbox via the
// controller store (Requirement 4.5).
func (s *Server) TerminateSandbox(ctx context.Context, req *pb.TerminateRequest) (*emptypb.Empty, error) {
	if req.BenchmarkRunId == "" {
		return nil, status.Error(codes.InvalidArgument, "benchmark_run_id is required")
	}

	s.log.Info("TerminateSandbox called",
		zap.String("benchmarkRunId", req.BenchmarkRunId),
		zap.String("reason", req.Reason),
	)

	if err := s.sandboxStore.TerminateSandbox(ctx, req.BenchmarkRunId, req.Reason); err != nil {
		s.log.Error("TerminateSandbox: store error",
			zap.String("benchmarkRunId", req.BenchmarkRunId),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, fmt.Sprintf("terminate sandbox: %v", err))
	}

	return &emptypb.Empty{}, nil
}

// endpointInfoToProto converts an endpoint.EndpointInfo to a proto Endpoint
// message.
func endpointInfoToProto(ep endpoint.EndpointInfo) *pb.Endpoint {
	proto := &pb.Endpoint{
		InternalIp:     ep.InternalIP,
		Port:           uint32(ep.Port),
		SubmissionId:   ep.SubmissionID,
		BenchmarkRunId: ep.BenchmarkRunID,
		Status:         pb.EndpointStatus_AVAILABLE,
	}

	switch ep.Protocol {
	case "FIX":
		proto.Protocol = pb.Protocol_FIX
	case "REST":
		proto.Protocol = pb.Protocol_REST
	case "WS":
		proto.Protocol = pb.Protocol_WEBSOCKET
	}

	return proto
}
