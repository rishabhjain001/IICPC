// Package grpc provides the BotFleetManager gRPC service implementation.
//
// Since protoc is unavailable in this environment, the gRPC stubs are
// hand-written. The server uses the types from generated/botfleet/v1 and
// communicates over channels for streaming.
package grpc

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	botfleetv1 "github.com/iicpc/dbhp/bot-fleet-manager/generated/botfleet/v1"
	"github.com/iicpc/dbhp/bot-fleet-manager/internal/provisioner"
	"github.com/iicpc/dbhp/bot-fleet-manager/internal/scheduler"
	"github.com/iicpc/dbhp/bot-fleet-manager/internal/store"
)

// Server implements the BotFleetManager service.
type Server struct {
	K8sClient kubernetes.Interface
	Logger    *zap.Logger
	Store     *store.FleetStore
}

// NewServer creates a new BotFleetManager gRPC server.
func NewServer(client kubernetes.Interface, logger *zap.Logger, fleetStore *store.FleetStore) *Server {
	return &Server{
		K8sClient: client,
		Logger:    logger,
		Store:     fleetStore,
	}
}

// ProvisionFleet schedules bots, provisions pods, and streams progress events.
//
// The stream argument is any implementation of botfleetv1.ProvisionEventSender
// which allows tests to inject a channel-based sender.
func (s *Server) ProvisionFleet(
	ctx context.Context,
	req *botfleetv1.ProvisionRequest,
	stream botfleetv1.ProvisionEventSender,
) error {
	runID := req.BenchmarkRunID
	fleetSize := int(req.BotCount)

	s.Logger.Info("ProvisionFleet requested",
		zap.String("run_id", runID),
		zap.Int("bot_count", fleetSize),
	)

	// Emit SCHEDULING event.
	if err := stream.Send(&botfleetv1.ProvisionEvent{
		ReadyCount: 0,
		TotalCount: req.BotCount,
		Phase:      "SCHEDULING",
		Message:    "Scheduling bots across worker nodes",
	}); err != nil {
		return fmt.Errorf("send scheduling event: %w", err)
	}

	// Fetch available worker nodes from Kubernetes.
	nodes, err := s.getWorkerNodes(ctx)
	if err != nil {
		return fmt.Errorf("get worker nodes: %w", err)
	}

	// Schedule bots using the scheduling algorithm.
	dist := scheduler.ScenarioDistribution{
		MarketMakerPct:     req.Distribution.MarketMakerPct,
		AggressiveTakerPct: req.Distribution.AggressiveTakerPct,
		CancelSpammerPct:   req.Distribution.CancelSpammerPct,
		MixedRetailPct:     req.Distribution.MixedRetailPct,
		LatencyProberPct:   req.Distribution.LatencyProberPct,
	}
	assignments, err := scheduler.Schedule(fleetSize, nodes, dist)
	if err != nil {
		_ = stream.Send(&botfleetv1.ProvisionEvent{
			ReadyCount: 0,
			TotalCount: req.BotCount,
			Phase:      "FAILED",
			Message:    fmt.Sprintf("Scheduling failed: %v", err),
		})
		return fmt.Errorf("scheduling: %w", err)
	}

	// Convert endpoint IDs to EndpointRef slice.
	endpoints := make([]botfleetv1.EndpointRef, 0, len(req.EndpointIDs))
	for _, id := range req.EndpointIDs {
		endpoints = append(endpoints, botfleetv1.EndpointRef{ID: id})
	}

	// Emit STARTING event.
	if err := stream.Send(&botfleetv1.ProvisionEvent{
		ReadyCount: 0,
		TotalCount: req.BotCount,
		Phase:      "STARTING",
		Message:    fmt.Sprintf("Deploying %d bot pods", fleetSize),
	}); err != nil {
		return fmt.Errorf("send starting event: %w", err)
	}

	// Provision the fleet.
	prov := &provisioner.Provisioner{
		K8sClient: s.K8sClient,
		Logger:    s.Logger,
		Store:     s.Store,
	}

	if err := prov.ProvisionFleet(ctx, runID, assignments, endpoints); err != nil {
		phase := "FAILED"
		if errors.Is(err, provisioner.ErrProvisioningTimeout) {
			phase = "TIMEOUT"
		}
		_ = stream.Send(&botfleetv1.ProvisionEvent{
			ReadyCount: 0,
			TotalCount: req.BotCount,
			Phase:      phase,
			Message:    err.Error(),
		})
		return err
	}

	// Emit final READY event.
	if err := stream.Send(&botfleetv1.ProvisionEvent{
		ReadyCount: req.BotCount,
		TotalCount: req.BotCount,
		Phase:      "READY",
		Message:    "All bots ready",
	}); err != nil {
		return fmt.Errorf("send ready event: %w", err)
	}

	s.Logger.Info("fleet provisioned successfully",
		zap.String("run_id", runID),
		zap.Int("fleet_size", fleetSize),
	)
	return nil
}

// StopFleet gracefully shuts down the fleet for a run (Requirement 5.6).
func (s *Server) StopFleet(ctx context.Context, req *botfleetv1.StopRequest) error {
	s.Logger.Info("StopFleet requested", zap.String("run_id", req.BenchmarkRunID))
	return provisioner.ShutdownFleet(ctx, s.K8sClient, req.BenchmarkRunID, s.Logger)
}

// GetFleetStatus returns the current fleet status for a run.
func (s *Server) GetFleetStatus(_ context.Context, req *botfleetv1.FleetStatusRequest) (*botfleetv1.FleetStatus, error) {
	record := s.Store.Get(req.BenchmarkRunID)
	if record == nil {
		return &botfleetv1.FleetStatus{BenchmarkRunID: req.BenchmarkRunID}, nil
	}
	return store.ToFleetStatus(record), nil
}

// getWorkerNodes queries Kubernetes for nodes tagged role=bot-worker and
// returns them as scheduler.WorkerNode values.
func (s *Server) getWorkerNodes(ctx context.Context) ([]scheduler.WorkerNode, error) {
	nodeList, err := s.K8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list worker nodes: %w", err)
	}

	nodes := make([]scheduler.WorkerNode, 0, len(nodeList.Items))
	for _, n := range nodeList.Items {
		if role, ok := n.Labels["role"]; !ok || role != "bot-worker" {
			continue
		}
		nodes = append(nodes, scheduler.WorkerNode{
			Name:        n.Name,
			CurrentBots: 0, // Populated from in-memory store in a production version.
			MaxCapacity: 500,
		})
	}
	return nodes, nil
}
