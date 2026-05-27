package endpoint_test

import (
	"context"
	"net"
	"testing"

	"github.com/iicpc/dbhp/sandbox-controller/internal/endpoint"
)

// listenTCP starts a TCP listener on a random loopback port and returns the
// listener together with its address string ("127.0.0.1:<port>").
// The listener is automatically closed when the test ends.
func listenTCP(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, ln.Addr().String()
}

// hostPort splits an address string into host and port, and converts the port
// to an int32.
func hostPort(t *testing.T, addr string) (string, int32) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("hostPort SplitHostPort: %v", err)
	}
	var port int
	if _, err := net.LookupPort("tcp", portStr); err == nil {
		// portStr is already numeric; parse it directly.
		if _, err2 := net.ResolveTCPAddr("tcp", addr); err2 != nil {
			t.Fatalf("hostPort resolve: %v", err2)
		}
	}
	// Parse the numeric port string.
	if _, err := net.LookupPort("tcp", portStr); err != nil {
		t.Fatalf("hostPort lookup port: %v", err)
	}
	portAddr, _ := net.ResolveTCPAddr("tcp", addr)
	port = portAddr.Port
	return host, int32(port)
}

// TestProbeEndpoints_Available verifies that an endpoint backed by a real
// listening TCP server is marked AVAILABLE.
func TestProbeEndpoints_Available(t *testing.T) {
	_, addr := listenTCP(t)
	host, port := hostPort(t, addr)

	eps := []endpoint.EndpointInfo{
		{Protocol: "REST", Port: port, InternalIP: host, Status: "UNAVAILABLE"},
	}

	called := false
	results := endpoint.ProbeEndpoints(context.Background(), eps, func() { called = true })

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != "AVAILABLE" {
		t.Errorf("expected AVAILABLE, got %q", results[0].Status)
	}
	if called {
		t.Error("onAllUnavailable should not have been called when endpoint is AVAILABLE")
	}
}

// TestProbeEndpoints_Unavailable verifies that an endpoint at a port where no
// server is listening is marked UNAVAILABLE.
func TestProbeEndpoints_Unavailable(t *testing.T) {
	// Find a free port, then close it immediately so nothing is listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // Close immediately — port is now free (nothing listening).

	host, port := hostPort(t, addr)

	eps := []endpoint.EndpointInfo{
		{Protocol: "FIX", Port: port, InternalIP: host, Status: ""},
	}

	results := endpoint.ProbeEndpoints(context.Background(), eps, nil)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != "UNAVAILABLE" {
		t.Errorf("expected UNAVAILABLE, got %q", results[0].Status)
	}
}

// TestProbeEndpoints_AllUnavailableTriggersCallback verifies that
// onAllUnavailable is called exactly once when every endpoint fails TCP
// connection (Requirement 4.6).
func TestProbeEndpoints_AllUnavailableTriggersCallback(t *testing.T) {
	// Open and immediately close two ports so nothing listens on them.
	makeClosedAddr := func(t *testing.T) (string, int32) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		return hostPort(t, addr)
	}

	host1, port1 := makeClosedAddr(t)
	host2, port2 := makeClosedAddr(t)

	eps := []endpoint.EndpointInfo{
		{Protocol: "FIX", Port: port1, InternalIP: host1},
		{Protocol: "REST", Port: port2, InternalIP: host2},
	}

	callCount := 0
	results := endpoint.ProbeEndpoints(context.Background(), eps, func() { callCount++ })

	for i, r := range results {
		if r.Status != "UNAVAILABLE" {
			t.Errorf("result[%d]: expected UNAVAILABLE, got %q", i, r.Status)
		}
	}
	if callCount != 1 {
		t.Errorf("onAllUnavailable called %d times; expected 1", callCount)
	}
}

// TestProbeEndpoints_PartialUnavailableNoCallback verifies that
// onAllUnavailable is NOT called when at least one endpoint is reachable
// (Requirement 4.6 — callback only fires when ALL are UNAVAILABLE).
func TestProbeEndpoints_PartialUnavailableNoCallback(t *testing.T) {
	// One real listener.
	_, availableAddr := listenTCP(t)
	availHost, availPort := hostPort(t, availableAddr)

	// One closed port.
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddr := closedLn.Addr().String()
	_ = closedLn.Close()
	closedHost, closedPort := hostPort(t, closedAddr)

	eps := []endpoint.EndpointInfo{
		{Protocol: "REST", Port: availPort, InternalIP: availHost},
		{Protocol: "FIX", Port: closedPort, InternalIP: closedHost},
	}

	called := false
	results := endpoint.ProbeEndpoints(context.Background(), eps, func() { called = true })

	if called {
		t.Error("onAllUnavailable must NOT be called when at least one endpoint is AVAILABLE")
	}

	// The available endpoint should be AVAILABLE; the closed one UNAVAILABLE.
	for _, r := range results {
		switch r.Protocol {
		case "REST":
			if r.Status != "AVAILABLE" {
				t.Errorf("REST endpoint: expected AVAILABLE, got %q", r.Status)
			}
		case "FIX":
			if r.Status != "UNAVAILABLE" {
				t.Errorf("FIX endpoint: expected UNAVAILABLE, got %q", r.Status)
			}
		}
	}
}
