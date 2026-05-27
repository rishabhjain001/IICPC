package rest

import (
	"encoding/json"
	"strings"
	"testing"
)

func newGen() *RESTGenerator {
	return &RESTGenerator{
		BenchmarkRunID: "run-test",
		BotID:          "bot-99",
	}
}

// TestRequestIDUniqueness verifies that request_id is unique per message.
func TestRequestIDUniqueness(t *testing.T) {
	g := newGen()
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		msg := g.PlaceOrder("BUY", "LIMIT", 100, 150.0)
		if seen[msg.RequestID] {
			t.Fatalf("duplicate RequestID %q at iteration %d", msg.RequestID, i)
		}
		seen[msg.RequestID] = true
	}
}

// TestSeqNumStrictlyIncreasing verifies seq nums are always increasing.
func TestSeqNumStrictlyIncreasing(t *testing.T) {
	g := newGen()
	prev := uint64(0)
	for i := 0; i < 50; i++ {
		var msg RESTMessage
		if i%2 == 0 {
			msg = g.PlaceOrder("BUY", "MARKET", 10, 0)
		} else {
			msg = g.CancelOrder("some-cl-ord-id")
		}
		if msg.SeqNum <= prev {
			t.Fatalf("SeqNum not strictly increasing: prev=%d, current=%d at i=%d", prev, msg.SeqNum, i)
		}
		prev = msg.SeqNum
	}
}

// TestSeqNumStartsAtOne verifies first sequence number is 1.
func TestSeqNumStartsAtOne(t *testing.T) {
	g := newGen()
	msg := g.PlaceOrder("BUY", "LIMIT", 100, 150.0)
	if msg.SeqNum != 1 {
		t.Fatalf("expected SeqNum=1, got %d", msg.SeqNum)
	}
}

// TestNanosecondTimestampEmbedded verifies send_time_ns is non-zero and embedded in JSON.
func TestNanosecondTimestampEmbedded(t *testing.T) {
	g := newGen()
	msg := g.PlaceOrder("BUY", "LIMIT", 100, 150.0)
	if msg.SendTimeNs == 0 {
		t.Fatal("SendTimeNs is zero")
	}
	// Verify it's in the JSON body.
	var decoded map[string]interface{}
	if err := json.Unmarshal(msg.Body, &decoded); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if _, ok := decoded["send_time_ns"]; !ok {
		t.Fatal("send_time_ns missing from JSON body")
	}
}

// TestPOSTBodyValidJSONWithRequiredFields verifies the POST body is valid JSON with all required fields.
func TestPOSTBodyValidJSONWithRequiredFields(t *testing.T) {
	g := newGen()
	msg := g.PlaceOrder("SELL", "LIMIT", 200, 99.5)

	if msg.Method != "POST" {
		t.Fatalf("expected method POST, got %s", msg.Method)
	}
	if msg.Path != "/v1/orders" {
		t.Fatalf("expected path /v1/orders, got %s", msg.Path)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(msg.Body, &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	required := []string{"request_id", "cl_ord_id", "seq_num", "send_time_ns", "symbol", "side", "ord_type", "qty", "price"}
	for _, field := range required {
		if _, ok := body[field]; !ok {
			t.Errorf("required field %q missing from POST body", field)
		}
	}

	// Validate specific values.
	if body["side"] != "SELL" {
		t.Errorf("expected side=SELL, got %v", body["side"])
	}
	if body["ord_type"] != "LIMIT" {
		t.Errorf("expected ord_type=LIMIT, got %v", body["ord_type"])
	}
	if qty, ok := body["qty"].(float64); !ok || qty != 200 {
		t.Errorf("expected qty=200, got %v", body["qty"])
	}
}

// TestMarketOrderBodyHasNoPrice verifies price is omitted for market orders (omitempty).
func TestMarketOrderBodyHasNoPrice(t *testing.T) {
	g := newGen()
	msg := g.PlaceOrder("BUY", "MARKET", 50, 0)

	var body map[string]interface{}
	if err := json.Unmarshal(msg.Body, &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if _, ok := body["price"]; ok {
		t.Error("price should be omitted for market orders (price=0)")
	}
}

// TestDELETEUsesCorrectPath verifies DELETE uses correct path with clOrdID.
func TestDELETEUsesCorrectPath(t *testing.T) {
	g := newGen()
	clOrdID := "run-test-bot-99-1"
	msg := g.CancelOrder(clOrdID)

	if msg.Method != "DELETE" {
		t.Fatalf("expected method DELETE, got %s", msg.Method)
	}
	expectedPath := "/v1/orders/" + clOrdID
	if msg.Path != expectedPath {
		t.Fatalf("expected path %q, got %q", expectedPath, msg.Path)
	}
	if !strings.HasSuffix(msg.Path, clOrdID) {
		t.Fatalf("path does not end with clOrdID %q: %q", clOrdID, msg.Path)
	}
}

// TestCancelOrderBodyEmpty verifies DELETE messages have no body.
func TestCancelOrderBodyEmpty(t *testing.T) {
	g := newGen()
	msg := g.CancelOrder("some-cl-ord-id")
	if len(msg.Body) != 0 {
		t.Fatalf("expected empty body for DELETE, got %q", string(msg.Body))
	}
}

// TestClOrdIDFormat verifies ClOrdID format is <runID>-<botID>-<seqNum> for PlaceOrder.
func TestClOrdIDFormat(t *testing.T) {
	g := &RESTGenerator{BenchmarkRunID: "run-xyz", BotID: "bot-7"}
	msg := g.PlaceOrder("BUY", "LIMIT", 10, 50.0)
	expected := "run-xyz-bot-7-1"
	if msg.ClOrdID != expected {
		t.Fatalf("expected ClOrdID=%q, got %q", expected, msg.ClOrdID)
	}
}

// TestRequestIDIsUUIDFormat verifies the request_id looks like a UUID.
func TestRequestIDIsUUIDFormat(t *testing.T) {
	g := newGen()
	msg := g.PlaceOrder("BUY", "LIMIT", 100, 150.0)
	// UUID format: 8-4-4-4-12 hex chars with dashes
	parts := strings.Split(msg.RequestID, "-")
	if len(parts) != 5 {
		t.Fatalf("request_id %q does not look like a UUID (expected 5 dash-separated parts)", msg.RequestID)
	}
	lengths := []int{8, 4, 4, 4, 12}
	for i, part := range parts {
		if len(part) != lengths[i] {
			t.Errorf("request_id part[%d] has length %d, expected %d: %q", i, len(part), lengths[i], part)
		}
	}
}
