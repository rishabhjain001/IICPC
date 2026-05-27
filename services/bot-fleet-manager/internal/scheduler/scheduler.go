// Package scheduler implements the bot fleet scheduling algorithm for the
// Bot Fleet Manager. It distributes bots across worker nodes and assigns
// scenario types according to a configured distribution.
package scheduler

import (
	"errors"
	"math"
	"sort"
)

// BotScenario represents a workload profile type.
type BotScenario string

const (
	ScenarioMarketMaker     BotScenario = "MARKET_MAKER"
	ScenarioAggressiveTaker BotScenario = "AGGRESSIVE_TAKER"
	ScenarioCancelSpammer   BotScenario = "CANCEL_SPAMMER"
	ScenarioMixedRetail     BotScenario = "MIXED_RETAIL"
	ScenarioLatencyProber   BotScenario = "LATENCY_PROBER"
)

// ErrInsufficientCapacity is returned when the total remaining capacity across
// all eligible worker nodes is less than the requested fleet size.
var ErrInsufficientCapacity = errors.New("insufficient node capacity for fleet")

// ErrNoAvailableNodes is returned when every worker node is already at its
// maximum capacity of 500 bots.
var ErrNoAvailableNodes = errors.New("no worker nodes have available capacity")

// ScenarioDistribution holds the percentage weights for each scenario type.
// The values do not need to sum to exactly 1.0 — they are treated as relative
// weights and normalised internally.
type ScenarioDistribution struct {
	MarketMakerPct     float32
	AggressiveTakerPct float32
	CancelSpammerPct   float32
	MixedRetailPct     float32
	LatencyProberPct   float32
}

// WorkerNode represents an available bot-worker Kubernetes node.
type WorkerNode struct {
	Name        string
	CurrentBots int
	MaxCapacity int // 500
}

// BotAssignment maps a bot index to a node name and scenario.
type BotAssignment struct {
	BotIndex int
	NodeName string
	Scenario BotScenario
}

// Schedule distributes fleetSize bots across nodes and assigns scenarios.
//
// Algorithm:
//  1. Filter nodes where CurrentBots < MaxCapacity (hard cap 500 enforced via MaxCapacity).
//  2. Sort ascending by CurrentBots (least-loaded first).
//  3. Assign bots round-robin across nodes while enforcing the per-node cap.
//  4. Assign scenarios using the largest-remainder method so total scenario
//     counts sum exactly to fleetSize.
//
// Returns ErrNoAvailableNodes if no nodes have remaining capacity.
// Returns ErrInsufficientCapacity if total remaining capacity < fleetSize.
func Schedule(fleetSize int, nodes []WorkerNode, dist ScenarioDistribution) ([]BotAssignment, error) {
	// Step 1: filter nodes with available capacity.
	eligible := make([]WorkerNode, 0, len(nodes))
	for _, n := range nodes {
		cap := n.MaxCapacity
		if cap <= 0 {
			cap = 500
		}
		if n.CurrentBots < cap {
			eligible = append(eligible, n)
		}
	}
	if len(eligible) == 0 {
		return nil, ErrNoAvailableNodes
	}

	// Check total available capacity.
	totalCap := 0
	for _, n := range eligible {
		cap := n.MaxCapacity
		if cap <= 0 {
			cap = 500
		}
		totalCap += cap - n.CurrentBots
	}
	if totalCap < fleetSize {
		return nil, ErrInsufficientCapacity
	}

	// Step 2: sort ascending by CurrentBots (least-loaded first).
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].CurrentBots < eligible[j].CurrentBots
	})

	// Build per-node remaining capacity tracking.
	nodeCaps := make([]int, len(eligible))
	for i, n := range eligible {
		cap := n.MaxCapacity
		if cap <= 0 {
			cap = 500
		}
		nodeCaps[i] = cap - n.CurrentBots
	}

	// Step 3 & 4: round-robin assignment while enforcing per-node cap.
	nodeAssignments := make([]string, fleetSize)
	nodeIdx := 0
	for i := 0; i < fleetSize; i++ {
		// Find the next node with remaining capacity using round-robin, skipping
		// any that are full.
		attempts := 0
		for nodeCaps[nodeIdx%len(eligible)] == 0 {
			nodeIdx++
			attempts++
			if attempts > len(eligible) {
				// Should not happen given totalCap check above.
				return nil, ErrInsufficientCapacity
			}
		}
		idx := nodeIdx % len(eligible)
		nodeAssignments[i] = eligible[idx].Name
		nodeCaps[idx]--
		nodeIdx++
	}

	// Step 5: scenario assignment using largest-remainder method.
	scenarios := assignScenarios(fleetSize, dist)

	assignments := make([]BotAssignment, fleetSize)
	for i := 0; i < fleetSize; i++ {
		assignments[i] = BotAssignment{
			BotIndex: i,
			NodeName: nodeAssignments[i],
			Scenario: scenarios[i],
		}
	}
	return assignments, nil
}

// assignScenarios uses the largest-remainder method to distribute scenarios
// such that the totals sum exactly to fleetSize and each scenario's share
// is as close to its weight as possible.
func assignScenarios(fleetSize int, dist ScenarioDistribution) []BotScenario {
	type scenarioWeight struct {
		scenario BotScenario
		weight   float64
	}

	total := float64(dist.MarketMakerPct) + float64(dist.AggressiveTakerPct) +
		float64(dist.CancelSpammerPct) + float64(dist.MixedRetailPct) +
		float64(dist.LatencyProberPct)

	// Avoid division by zero: if all weights are zero, distribute uniformly.
	if total == 0 {
		total = 5
		dist.MarketMakerPct = 1
		dist.AggressiveTakerPct = 1
		dist.CancelSpammerPct = 1
		dist.MixedRetailPct = 1
		dist.LatencyProberPct = 1
	}

	sw := []scenarioWeight{
		{ScenarioMarketMaker, float64(dist.MarketMakerPct) / total * float64(fleetSize)},
		{ScenarioAggressiveTaker, float64(dist.AggressiveTakerPct) / total * float64(fleetSize)},
		{ScenarioCancelSpammer, float64(dist.CancelSpammerPct) / total * float64(fleetSize)},
		{ScenarioMixedRetail, float64(dist.MixedRetailPct) / total * float64(fleetSize)},
		{ScenarioLatencyProber, float64(dist.LatencyProberPct) / total * float64(fleetSize)},
	}

	// Floor counts.
	counts := make([]int, len(sw))
	remainders := make([]float64, len(sw))
	allocated := 0
	for i, s := range sw {
		counts[i] = int(math.Floor(s.weight))
		remainders[i] = s.weight - float64(counts[i])
		allocated += counts[i]
	}

	// Distribute remaining slots by largest remainder.
	remaining := fleetSize - allocated
	type idxRemainder struct {
		idx       int
		remainder float64
	}
	sorted := make([]idxRemainder, len(sw))
	for i := range sw {
		sorted[i] = idxRemainder{i, remainders[i]}
	}
	sort.Slice(sorted, func(a, b int) bool {
		return sorted[a].remainder > sorted[b].remainder
	})
	for i := 0; i < remaining; i++ {
		counts[sorted[i].idx]++
	}

	// Build the scenario slice: fill in order (all of scenario 0, then 1, …)
	// then interleave by cycling so consecutive bots get different scenarios.
	result := make([]BotScenario, 0, fleetSize)
	for i, s := range sw {
		for j := 0; j < counts[i]; j++ {
			result = append(result, s.scenario)
		}
	}

	// Interleave: reorder so bots are spread across scenario types rather than
	// packed together (round-robin by scenario order).
	interleaved := make([]BotScenario, fleetSize)
	pos := 0
	for round := 0; ; round++ {
		advanced := false
		for i, s := range sw {
			if round < counts[i] {
				interleaved[pos] = s.scenario
				pos++
				advanced = true
			}
		}
		if !advanced {
			break
		}
	}

	return interleaved
}
