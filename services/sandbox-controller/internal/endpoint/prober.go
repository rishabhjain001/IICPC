package endpoint

import (
	"context"
	"fmt"
	"net"
	"time"
)

// probeTimeout is the per-endpoint TCP dial timeout (Requirement 4.3).
const probeTimeout = 15 * time.Second

// ProbeEndpoints attempts a TCP connection to each endpoint within a 15-second
// window, marking unresponsive ones UNAVAILABLE (Requirement 4.3, 4.4).
//
// After probing every endpoint:
//   - If ALL endpoints are UNAVAILABLE, onAllUnavailable is called exactly once
//     (Requirement 4.6).
//
// The returned slice has the same order as the input, with Status fields
// updated to "AVAILABLE" or "UNAVAILABLE".
func ProbeEndpoints(ctx context.Context, endpoints []EndpointInfo, onAllUnavailable func()) []EndpointInfo {
	results := make([]EndpointInfo, len(endpoints))
	copy(results, endpoints)

	for i, ep := range results {
		addr := fmt.Sprintf("%s:%d", ep.InternalIP, ep.Port)

		// Use a derived context so the dial respects any parent cancellation,
		// but still enforces the 15-second per-endpoint timeout.
		dialCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
		cancel()

		if err != nil {
			results[i].Status = "UNAVAILABLE"
		} else {
			_ = conn.Close()
			results[i].Status = "AVAILABLE"
		}
	}

	// Check whether every endpoint is unavailable (Requirement 4.6).
	allUnavailable := len(results) > 0
	for _, ep := range results {
		if ep.Status != "UNAVAILABLE" {
			allUnavailable = false
			break
		}
	}
	if allUnavailable && onAllUnavailable != nil {
		onAllUnavailable()
	}

	return results
}
