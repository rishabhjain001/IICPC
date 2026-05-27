package validator

import (
	"testing"
)

// --- mock RME ----------------------------------------------------------------

// mockRME is a test double for RMEInvoker that returns a preconfigured result.
type mockRME struct {
	result *MatchResult
	err    error
}

func (m *mockRME) RunMatchingEngine(_ []OrderEvent) (*MatchResult, error) {
	return m.result, m.err
}

// Verify mockRME satisfies the interface at compile time.
var _ RMEInvoker = (*mockRME)(nil)

// --- helper ------------------------------------------------------------------

// mustNoViolations fails the test if any violations are returned.
func mustNoViolations(t *testing.T, violations []ViolationType) {
	t.Helper()
	if len(violations) != 0 {
		t.Errorf("expected no violations, got %v", violations)
	}
}

// mustHaveViolation fails the test if the expected violation is not present.
func mustHaveViolation(t *testing.T, violations []ViolationType, want ViolationType) {
	t.Helper()
	for _, v := range violations {
		if v == want {
			return
		}
	}
	t.Errorf("expected violation %q, got %v", want, violations)
}

// --- Task 11.4 unit tests ----------------------------------------------------

// TestCorrectFillHasNoViolations verifies that a fill matching the expected
// price, quantity, and resting order produces an empty violation list
// (Requirement 8.1).
func TestCorrectFillHasNoViolations(t *testing.T) {
	v := &FillValidator{RME: &mockRME{}}

	sandbox := SandboxFill{
		OrderID:        "ord-1",
		Price:          "100.00",
		Qty:            50,
		RestingOrderID: "rest-1",
	}
	expected := ExpectedFill{
		OrderID:        "ord-1",
		Price:          "100.00",
		Qty:            50,
		RestingOrderID: "rest-1",
	}

	violations := v.ValidateFill(sandbox, expected)
	mustNoViolations(t, violations)
}

// TestWrongPricePricePriorityViolation verifies that a fill price different
// from the expected price produces a PRICE_PRIORITY_VIOLATION (Requirement 8.2).
func TestWrongPricePricePriorityViolation(t *testing.T) {
	v := &FillValidator{RME: &mockRME{}}

	sandbox := SandboxFill{
		OrderID:        "ord-2",
		Price:          "99.00", // worse fill price
		Qty:            50,
		RestingOrderID: "rest-1",
	}
	expected := ExpectedFill{
		OrderID:        "ord-2",
		Price:          "100.00",
		Qty:            50,
		RestingOrderID: "rest-1",
	}

	violations := v.ValidateFill(sandbox, expected)
	mustHaveViolation(t, violations, PricePriorityViolation)
}

// TestWrongRestingOrderTimePriorityViolation verifies that filling a different
// resting order than the earliest-arriving produces a TIME_PRIORITY_VIOLATION
// (Requirement 8.3).
func TestWrongRestingOrderTimePriorityViolation(t *testing.T) {
	v := &FillValidator{RME: &mockRME{}}

	sandbox := SandboxFill{
		OrderID:        "ord-3",
		Price:          "100.00",
		Qty:            50,
		RestingOrderID: "rest-2", // not the earliest-arriving resting order
	}
	expected := ExpectedFill{
		OrderID:        "ord-3",
		Price:          "100.00",
		Qty:            50,
		RestingOrderID: "rest-1", // expected earliest-arriving resting order
	}

	violations := v.ValidateFill(sandbox, expected)
	mustHaveViolation(t, violations, TimePriorityViolation)
}

// TestWrongQuantityQuantityMismatch verifies that a filled quantity different
// from the expected quantity produces a QUANTITY_MISMATCH (Requirement 8.4).
func TestWrongQuantityQuantityMismatch(t *testing.T) {
	v := &FillValidator{RME: &mockRME{}}

	sandbox := SandboxFill{
		OrderID:        "ord-4",
		Price:          "100.00",
		Qty:            30, // short fill
		RestingOrderID: "rest-1",
	}
	expected := ExpectedFill{
		OrderID:        "ord-4",
		Price:          "100.00",
		Qty:            50,
		RestingOrderID: "rest-1",
	}

	violations := v.ValidateFill(sandbox, expected)
	mustHaveViolation(t, violations, QuantityMismatch)
}

// TestMultipleViolationsReturned verifies that all applicable violations are
// returned when a fill has both a price mismatch and a quantity mismatch.
func TestMultipleViolationsReturned(t *testing.T) {
	v := &FillValidator{RME: &mockRME{}}

	sandbox := SandboxFill{
		OrderID:        "ord-5",
		Price:          "95.00",  // wrong price
		Qty:            20,       // wrong qty
		RestingOrderID: "rest-1", // correct resting order
	}
	expected := ExpectedFill{
		OrderID:        "ord-5",
		Price:          "100.00",
		Qty:            50,
		RestingOrderID: "rest-1",
	}

	violations := v.ValidateFill(sandbox, expected)
	mustHaveViolation(t, violations, PricePriorityViolation)
	mustHaveViolation(t, violations, QuantityMismatch)

	// Should NOT have time-priority violation since resting order matches.
	for _, v := range violations {
		if v == TimePriorityViolation {
			t.Errorf("unexpected TIME_PRIORITY_VIOLATION when resting order matches")
		}
	}
}

// TestComputeAccuracyScore_NormalCase verifies ComputeAccuracyScore(10, 8) = 0.8
// (Requirement 8.5).
func TestComputeAccuracyScore_NormalCase(t *testing.T) {
	got := ComputeAccuracyScore(10, 8)
	want := 0.8
	if got != want {
		t.Errorf("ComputeAccuracyScore(10, 8): got %v, want %v", got, want)
	}
}

// TestComputeAccuracyScore_NoFills verifies ComputeAccuracyScore(0, 0) = 0.0
// (Requirement 8.5: no fills → score is 0.0).
func TestComputeAccuracyScore_NoFills(t *testing.T) {
	got := ComputeAccuracyScore(0, 0)
	if got != 0.0 {
		t.Errorf("ComputeAccuracyScore(0, 0): got %v, want 0.0", got)
	}
}

// TestComputeAccuracyScore_AllCorrect verifies perfect accuracy = 1.0.
func TestComputeAccuracyScore_AllCorrect(t *testing.T) {
	got := ComputeAccuracyScore(5, 5)
	if got != 1.0 {
		t.Errorf("ComputeAccuracyScore(5, 5): got %v, want 1.0", got)
	}
}

// TestComputeAccuracyScore_NoneCorrect verifies all-wrong accuracy = 0.0.
func TestComputeAccuracyScore_NoneCorrect(t *testing.T) {
	got := ComputeAccuracyScore(5, 0)
	if got != 0.0 {
		t.Errorf("ComputeAccuracyScore(5, 0): got %v, want 0.0", got)
	}
}

// TestComputeAccuracyScore_Clamped verifies that correctFills > totalFills
// is clamped to 1.0 (guard against caller bugs).
func TestComputeAccuracyScore_Clamped(t *testing.T) {
	got := ComputeAccuracyScore(5, 10)
	if got != 1.0 {
		t.Errorf("ComputeAccuracyScore(5, 10): got %v, want 1.0 (clamped)", got)
	}
}
