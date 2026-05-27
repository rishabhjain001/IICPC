// Package validator implements correctness validation of Sandbox fill events
// against the Reference Matching Engine (Requirement 8.1–8.5).
//
// The RME is invoked through the RMEInvoker interface so the validator can be
// unit-tested without the Rust toolchain or libdbhp_rme.so present.  The
// production implementation of RMEInvoker calls the Rust library via CGo FFI.
package validator

// ViolationType labels a specific fill correctness violation.
// Corresponds to the violation_flags stored in the fill_events hypertable.
type ViolationType string

const (
	// PricePriorityViolation is raised when the Sandbox assigns a fill at a
	// price that is worse than what the Reference Matching Engine computed for
	// the same order book state (Requirement 8.2).
	PricePriorityViolation ViolationType = "PRICE_PRIORITY_VIOLATION"
	// TimePriorityViolation is raised when the Sandbox fills a later-arriving
	// order at a given price level before an earlier-arriving order
	// (Requirement 8.3).
	TimePriorityViolation ViolationType = "TIME_PRIORITY_VIOLATION"
	// QuantityMismatch is raised when the filled quantity reported by the
	// Sandbox differs from the expected quantity (Requirement 8.4).
	QuantityMismatch ViolationType = "QUANTITY_MISMATCH"
)

// OrderEvent is an input record fed to the Reference Matching Engine.
// It represents a single order (new, cancel, or replace) in the sequence
// that preceded the fill under validation.
type OrderEvent struct {
	// OrderID is the platform-assigned identifier for this order.
	OrderID string
	// Side is "BUY" or "SELL".
	Side string
	// Price is the limit price as a decimal string.  Empty for market orders.
	Price string
	// Qty is the order quantity in shares / lots.
	Qty int64
	// SeqNum is the monotonic sequence number used for time-priority ordering.
	SeqNum int64
}

// MatchResult is the output produced by the Reference Matching Engine for a
// given sequence of OrderEvents.
type MatchResult struct {
	// Fills lists every expected fill event in the order they were generated.
	Fills []ExpectedFill
}

// ExpectedFill is the fill outcome the Reference Matching Engine computed for
// a single order.  It is compared against the corresponding SandboxFill.
type ExpectedFill struct {
	// OrderID identifies the order that was filled.
	OrderID string
	// Price is the execution price as a decimal string.
	Price string
	// Qty is the expected filled quantity.
	Qty int64
	// RestingOrderID is the ID of the resting (passive) order that was matched
	// against.  Used to validate time-priority: if a different resting order
	// was matched first it indicates a time-priority violation.
	RestingOrderID string
}

// SandboxFill is the fill event reported by the Sandbox submission.
type SandboxFill struct {
	// OrderID identifies the order that was filled.
	OrderID string
	// Price is the execution price as a decimal string.
	Price string
	// Qty is the filled quantity as reported by the Sandbox.
	Qty int64
	// RestingOrderID is the ID of the resting order that was matched, as
	// reported by the Sandbox.  Used to check time-priority.
	RestingOrderID string
}

// RMEInvoker is the interface for invoking the Reference Matching Engine.
//
// Production implementation: calls libdbhp_rme.so via CGo FFI, marshals
// []OrderEvent to a C-compatible struct array, and unmarshals the returned
// fill list.
//
// Test implementation: a mock that returns a pre-configured MatchResult.
type RMEInvoker interface {
	// RunMatchingEngine runs the deterministic matching engine over the given
	// sequence of order events and returns the expected fill outcome.
	RunMatchingEngine(events []OrderEvent) (*MatchResult, error)
}

// FillValidator compares Sandbox fill events against the expected results
// produced by the Reference Matching Engine (Requirement 8.1).
type FillValidator struct {
	// RME is the Reference Matching Engine invoker.  Set to a real CGo
	// implementation in production; use a mock in tests.
	RME RMEInvoker
}

// ValidateFill compares a single Sandbox fill against the expected fill from
// the RME and returns the list of violations (empty slice = correct fill).
//
// Violation rules (Requirements 8.2–8.4):
//   - PRICE_PRIORITY_VIOLATION: sandboxFill.Price != expected.Price
//   - TIME_PRIORITY_VIOLATION:  sandboxFill.RestingOrderID != expected.RestingOrderID
//   - QUANTITY_MISMATCH:        sandboxFill.Qty != expected.Qty
func (v *FillValidator) ValidateFill(sandboxFill SandboxFill, expected ExpectedFill) []ViolationType {
	var violations []ViolationType

	if sandboxFill.Price != expected.Price {
		violations = append(violations, PricePriorityViolation)
	}

	if sandboxFill.RestingOrderID != expected.RestingOrderID {
		violations = append(violations, TimePriorityViolation)
	}

	if sandboxFill.Qty != expected.Qty {
		violations = append(violations, QuantityMismatch)
	}

	return violations
}

// ComputeAccuracyScore returns the Fill Accuracy Score as the ratio of correct
// fills to total fills (Requirement 8.5).
//
//   - A fill is correct if and only if it has no associated violation flag.
//   - The score is in the range [0.0, 1.0].
//   - If totalFills is 0, the score is 0.0 (no fills → no accuracy to measure).
func ComputeAccuracyScore(totalFills, correctFills int) float64 {
	if totalFills <= 0 {
		return 0.0
	}
	if correctFills < 0 {
		correctFills = 0
	}
	if correctFills > totalFills {
		correctFills = totalFills
	}
	return float64(correctFills) / float64(totalFills)
}
