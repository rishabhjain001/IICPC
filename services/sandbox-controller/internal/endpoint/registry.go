// Package endpoint implements endpoint registration, retrieval, and TCP probe
// logic for the Sandbox Controller.
//
// Requirements: 4.1, 4.2, 4.3, 4.4, 4.6
package endpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Well-known TCP port numbers for each supported protocol (Requirement 4.1).
const (
	PortFIX       = 9898
	PortREST      = 8080
	PortWebSocket = 8081
)

// endpointRegistryTTL is the Redis TTL applied to the session endpoints hash.
// In production this should be at least as long as the max sandbox lifetime
// (7200 s); a generous 3-hour window is used here.
const endpointRegistryTTL = 3 * time.Hour

// EndpointInfo describes a single protocol endpoint exposed by a sandboxed
// submission.
type EndpointInfo struct {
	Protocol       string // "FIX" | "REST" | "WS"
	Port           int32
	Status         string // "AVAILABLE" | "UNAVAILABLE"
	InternalIP     string
	SubmissionID   string
	BenchmarkRunID string
}

// Registry stores and retrieves endpoint metadata in Redis.
//
// The backing data structure is a Redis Hash keyed by
// "session:{runID}:endpoints" where each field is the protocol name and the
// value is the JSON-encoded EndpointInfo (Requirement 4.2).
type Registry struct {
	rdb redis.Cmdable
}

// NewRegistry creates a Registry backed by the provided Redis client.
func NewRegistry(rdb redis.Cmdable) *Registry {
	return &Registry{rdb: rdb}
}

// RegisterEndpoints stores endpoint metadata in Redis under
// "session:{runID}:endpoints" within 5 seconds of RUNNING state
// (Requirement 4.2).  The call uses a 5-second context deadline to enforce
// the latency requirement.
func (r *Registry) RegisterEndpoints(ctx context.Context, runID string, endpoints []EndpointInfo) error {
	// Enforce the 5-second registration deadline (Requirement 4.2).
	regCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	key := fmt.Sprintf("session:%s:endpoints", runID)

	// Build the Hash field map: protocol → JSON.
	fields := make(map[string]interface{}, len(endpoints))
	for _, ep := range endpoints {
		b, err := json.Marshal(ep)
		if err != nil {
			return fmt.Errorf("RegisterEndpoints: marshal endpoint %s: %w", ep.Protocol, err)
		}
		fields[ep.Protocol] = string(b)
	}

	pipe := r.rdb.Pipeline()
	pipe.HSet(regCtx, key, fields)
	pipe.Expire(regCtx, key, endpointRegistryTTL)
	if _, err := pipe.Exec(regCtx); err != nil {
		return fmt.Errorf("RegisterEndpoints: redis pipeline: %w", err)
	}
	return nil
}

// GetEndpoints retrieves all endpoints stored for a Benchmark Run.  Returns
// an empty slice (not an error) when no endpoints have been registered yet.
func (r *Registry) GetEndpoints(ctx context.Context, runID string) ([]EndpointInfo, error) {
	key := fmt.Sprintf("session:%s:endpoints", runID)

	vals, err := r.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("GetEndpoints: hgetall: %w", err)
	}

	endpoints := make([]EndpointInfo, 0, len(vals))
	for _, v := range vals {
		var ep EndpointInfo
		if err := json.Unmarshal([]byte(v), &ep); err != nil {
			return nil, fmt.Errorf("GetEndpoints: unmarshal endpoint: %w", err)
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

// UpdateEndpointStatus writes a single endpoint's status back to Redis.
// This is used after TCP probing to mark endpoints AVAILABLE or UNAVAILABLE
// (Requirement 4.4).
func (r *Registry) UpdateEndpointStatus(ctx context.Context, runID string, ep EndpointInfo) error {
	key := fmt.Sprintf("session:%s:endpoints", runID)

	b, err := json.Marshal(ep)
	if err != nil {
		return fmt.Errorf("UpdateEndpointStatus: marshal: %w", err)
	}
	return r.rdb.HSet(ctx, key, ep.Protocol, string(b)).Err()
}
