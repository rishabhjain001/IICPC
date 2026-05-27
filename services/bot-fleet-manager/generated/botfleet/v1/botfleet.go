// Package botfleetv1 contains hand-written Go types that mirror the protobuf
// definitions in proto/dbhp/botfleet/v1/botfleet.proto.
//
// These types are used in place of protoc-generated code because the build
// environment does not have protoc available. The types are intentionally kept
// simple and do not implement the full proto.Message interface.
package botfleetv1

import "context"

// ProvisionRequest is sent by the Control Plane to request a bot fleet.
type ProvisionRequest struct {
	BenchmarkRunID string
	BotCount       uint32
	Distribution   ScenarioDistribution
	EndpointIDs    []string
}

// ScenarioDistribution defines the relative weights for each bot scenario.
// Values are normalised internally; they do not need to sum to 1.0.
type ScenarioDistribution struct {
	MarketMakerPct     float32
	AggressiveTakerPct float32
	CancelSpammerPct   float32
	MixedRetailPct     float32
	LatencyProberPct   float32
}

// ProvisionEvent is streamed back to the caller as bots transition states.
type ProvisionEvent struct {
	ReadyCount uint32
	TotalCount uint32
	// Phase is one of: SCHEDULING | STARTING | READY | TIMEOUT | FAILED
	Phase   string
	Message string
}

// StopRequest is sent to gracefully terminate a fleet.
type StopRequest struct {
	BenchmarkRunID string
}

// FleetStatusRequest requests the current fleet summary.
type FleetStatusRequest struct {
	BenchmarkRunID string
}

// FleetStatus is the aggregate summary of a running fleet.
type FleetStatus struct {
	BenchmarkRunID    string
	TotalBots         uint32
	ReadyBots         uint32
	RunningBots       uint32
	ThrottlingBots    uint32
	TerminatedBots    uint32
	AggregateMsgPerSec uint64
}

// ProvisionEventSender is a channel-based helper that the gRPC server uses to
// stream ProvisionEvents to a caller.
type ProvisionEventSender interface {
	Send(event *ProvisionEvent) error
	Context() context.Context
}

// EndpointRef is a resolved sandbox endpoint that bots connect to.
type EndpointRef struct {
	ID         string // sandbox internal IP or identifier
	InternalIP string
	Port       uint32
	Protocol   string // FIX | REST | WEBSOCKET
}
