package scheduler

import (
	"errors"
	"testing"
)

// equalDist is a convenience ScenarioDistribution with 20% each.
var equalDist = ScenarioDistribution{
	MarketMakerPct:     0.20,
	AggressiveTakerPct: 0.20,
	CancelSpammerPct:   0.20,
	MixedRetailPct:     0.20,
	LatencyProberPct:   0.20,
}

func makeNodes(names []string, current, maxCap int) []WorkerNode {
	nodes := make([]WorkerNode, len(names))
	for i, n := range names {
		nodes[i] = WorkerNode{Name: n, CurrentBots: current, MaxCapacity: maxCap}
	}
	return nodes
}

// TestTotalAssignmentsEqualsFleetSize verifies that Schedule returns exactly
// fleetSize assignments for a normal scenario.
func TestTotalAssignmentsEqualsFleetSize(t *testing.T) {
	// 3 nodes × 500 cap = 1500 max. Use sizes that fit.
	nodes := makeNodes([]string{"node-a", "node-b", "node-c"}, 0, 500)
	for _, size := range []int{100, 500, 1000, 1500} {
		assignments, err := Schedule(size, nodes, equalDist)
		if err != nil {
			t.Fatalf("fleetSize=%d: unexpected error: %v", size, err)
		}
		if len(assignments) != size {
			t.Errorf("fleetSize=%d: got %d assignments, want %d", size, len(assignments), size)
		}
	}

	// Verify that exceeding total capacity returns ErrInsufficientCapacity.
	_, err := Schedule(1501, nodes, equalDist)
	if !errors.Is(err, ErrInsufficientCapacity) {
		t.Errorf("expected ErrInsufficientCapacity for 1501 bots on 3×500 nodes, got %v", err)
	}
}

// TestPerNodeCapOf500IsEnforced verifies that no node receives more than 500 bots.
func TestPerNodeCapOf500IsEnforced(t *testing.T) {
	// 3 nodes, each already at 0 bots, MaxCapacity=500. Fleet of 1400 (near 3×500).
	nodes := makeNodes([]string{"node-a", "node-b", "node-c"}, 0, 500)
	assignments, err := Schedule(1400, nodes, equalDist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	counts := map[string]int{}
	for _, a := range assignments {
		counts[a.NodeName]++
	}
	for name, cnt := range counts {
		if cnt > 500 {
			t.Errorf("node %s received %d bots, exceeds cap of 500", name, cnt)
		}
	}
}

// TestRoundRobinDistribution verifies that bots are spread roughly evenly
// across nodes when starting from empty.
func TestRoundRobinDistribution(t *testing.T) {
	nodes := makeNodes([]string{"node-a", "node-b", "node-c"}, 0, 500)
	fleetSize := 300
	assignments, err := Schedule(fleetSize, nodes, equalDist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	counts := map[string]int{}
	for _, a := range assignments {
		counts[a.NodeName]++
	}
	// Each node should receive exactly 100 bots.
	for _, name := range []string{"node-a", "node-b", "node-c"} {
		if counts[name] != 100 {
			t.Errorf("node %s: got %d bots, want 100", name, counts[name])
		}
	}
}

// TestScenarioDistributionSumsToFleetSize verifies that scenario counts add up
// to fleetSize.
func TestScenarioDistributionSumsToFleetSize(t *testing.T) {
	nodes := makeNodes([]string{"node-a", "node-b"}, 0, 500)
	fleetSize := 1000
	assignments, err := Schedule(fleetSize, nodes, equalDist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	scenarioCounts := map[BotScenario]int{}
	for _, a := range assignments {
		scenarioCounts[a.Scenario]++
	}
	total := 0
	for _, cnt := range scenarioCounts {
		total += cnt
	}
	if total != fleetSize {
		t.Errorf("scenario total %d != fleetSize %d", total, fleetSize)
	}
}

// TestScenarioDeviation verifies that for a fleet of 100+ bots each scenario
// is represented within ±5% of the configured distribution.
func TestScenarioDeviation(t *testing.T) {
	nodes := makeNodes([]string{"node-a", "node-b", "node-c"}, 0, 500)
	fleetSize := 1000
	assignments, err := Schedule(fleetSize, nodes, equalDist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	scenarioCounts := map[BotScenario]int{}
	for _, a := range assignments {
		scenarioCounts[a.Scenario]++
	}

	expectedPct := 0.20
	tolerance := 0.05
	for _, s := range []BotScenario{
		ScenarioMarketMaker, ScenarioAggressiveTaker, ScenarioCancelSpammer,
		ScenarioMixedRetail, ScenarioLatencyProber,
	} {
		actual := float64(scenarioCounts[s]) / float64(fleetSize)
		deviation := actual - expectedPct
		if deviation < 0 {
			deviation = -deviation
		}
		if deviation > tolerance {
			t.Errorf("scenario %s: actual %.3f, expected %.3f, deviation %.3f exceeds tolerance %.3f",
				s, actual, expectedPct, deviation, tolerance)
		}
	}
}

// TestErrInsufficientCapacity verifies that ErrInsufficientCapacity is
// returned when nodes don't have enough total capacity.
func TestErrInsufficientCapacity(t *testing.T) {
	// 2 nodes each with 490 current bots → 10 remaining each = 20 total.
	nodes := makeNodes([]string{"node-a", "node-b"}, 490, 500)
	_, err := Schedule(100, nodes, equalDist)
	if !errors.Is(err, ErrInsufficientCapacity) {
		t.Errorf("expected ErrInsufficientCapacity, got %v", err)
	}
}

// TestErrNoAvailableNodes verifies that ErrNoAvailableNodes is returned when
// all nodes are at maximum capacity.
func TestErrNoAvailableNodes(t *testing.T) {
	// All nodes at full capacity.
	nodes := makeNodes([]string{"node-a", "node-b", "node-c"}, 500, 500)
	_, err := Schedule(10, nodes, equalDist)
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Errorf("expected ErrNoAvailableNodes, got %v", err)
	}
}

// TestNodPartiallyFull verifies that nodes already partially loaded still
// respect the 500-bot cap.
func TestNodePartiallyFull(t *testing.T) {
	// node-a has 480 bots, node-b has 0.  Fleet of 40 should overflow node-a
	// (only 20 spots) so extra bots go to node-b.
	nodes := []WorkerNode{
		{Name: "node-a", CurrentBots: 480, MaxCapacity: 500},
		{Name: "node-b", CurrentBots: 0, MaxCapacity: 500},
	}
	assignments, err := Schedule(40, nodes, equalDist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	counts := map[string]int{}
	for _, a := range assignments {
		counts[a.NodeName]++
	}
	if counts["node-a"] > 20 {
		t.Errorf("node-a should receive at most 20 bots (cap 500, current 480), got %d", counts["node-a"])
	}
	if len(assignments) != 40 {
		t.Errorf("expected 40 assignments, got %d", len(assignments))
	}
}

// TestEmptyNodes verifies ErrNoAvailableNodes with empty node list.
func TestEmptyNodes(t *testing.T) {
	_, err := Schedule(100, []WorkerNode{}, equalDist)
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Errorf("expected ErrNoAvailableNodes for empty nodes, got %v", err)
	}
}
