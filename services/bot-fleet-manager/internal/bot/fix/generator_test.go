package fix

import (
	"strings"
	"testing"
)

func newGen() *FIXGenerator {
	return &FIXGenerator{
		BenchmarkRunID: "run-abc",
		BotID:          "bot-1",
	}
}

// TestClOrdIDUniqueness verifies that ClOrdIDs are unique across multiple calls.
func TestClOrdIDUniqueness(t *testing.T) {
	g := newGen()
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		msg := g.LimitOrder("1", 100, 150.0)
		if seen[msg.ClOrdID] {
			t.Fatalf("duplicate ClOrdID %q at iteration %d", msg.ClOrdID, i)
		}
		seen[msg.ClOrdID] = true
	}
}

// TestSeqNumStartsAtOne verifies the first sequence number is 1.
func TestSeqNumStartsAtOne(t *testing.T) {
	g := newGen()
	msg := g.LimitOrder("1", 100, 150.0)
	if msg.SeqNum != 1 {
		t.Fatalf("expected SeqNum=1, got %d", msg.SeqNum)
	}
}

// TestSeqNumStrictlyIncreasing verifies sequence numbers are always incrementing.
func TestSeqNumStrictlyIncreasing(t *testing.T) {
	g := newGen()
	prev := uint64(0)
	for i := 0; i < 50; i++ {
		var msg FIXMessage
		switch i % 3 {
		case 0:
			msg = g.LimitOrder("1", 100, 150.0)
		case 1:
			msg = g.MarketOrder("2", 50)
		case 2:
			msg = g.CancelReplace("prev-id", "1", 200, 151.0)
		}
		if msg.SeqNum <= prev {
			t.Fatalf("SeqNum not strictly increasing: prev=%d, current=%d at i=%d", prev, msg.SeqNum, i)
		}
		prev = msg.SeqNum
	}
}

// TestFIX42Header verifies messages begin with FIX 4.2 header.
func TestFIX42Header(t *testing.T) {
	g := newGen()
	msgs := []FIXMessage{
		g.LimitOrder("1", 100, 150.0),
		g.MarketOrder("2", 50),
		g.CancelReplace("orig-1", "1", 100, 150.0),
	}
	for _, msg := range msgs {
		if !strings.HasPrefix(string(msg.Raw), "8=FIX.4.2\x01") {
			t.Fatalf("message does not start with FIX 4.2 header: %q", string(msg.Raw))
		}
	}
}

// TestChecksumCorrect verifies that the embedded checksum matches the computed value.
func TestChecksumCorrect(t *testing.T) {
	g := newGen()
	msgs := []FIXMessage{
		g.LimitOrder("1", 100, 150.0),
		g.MarketOrder("2", 50),
		g.CancelReplace("orig-id", "2", 300, 99.5),
	}
	for i, msg := range msgs {
		_, ok := verifyChecksum(msg.Raw)
		if !ok {
			t.Fatalf("checksum mismatch for message[%d]: %q", i, string(msg.Raw))
		}
	}
}

// TestNanosecondTimestampNonZero verifies that SendTimeNs is embedded and non-zero.
func TestNanosecondTimestampNonZero(t *testing.T) {
	g := newGen()
	msgs := []FIXMessage{
		g.LimitOrder("1", 100, 150.0),
		g.MarketOrder("2", 50),
		g.CancelReplace("orig-2", "1", 400, 200.0),
	}
	for i, msg := range msgs {
		if msg.SendTimeNs == 0 {
			t.Fatalf("message[%d] has zero SendTimeNs", i)
		}
		// The raw bytes must contain the timestamp as a string.
		needle := "\x0152="
		raw := string(msg.Raw)
		if !strings.Contains(raw, needle) {
			t.Fatalf("message[%d] raw bytes do not contain tag 52: %q", i, raw)
		}
	}
}

// TestLimitOrderContainsRequiredTags verifies all mandatory fields for a limit order.
func TestLimitOrderContainsRequiredTags(t *testing.T) {
	g := newGen()
	msg := g.LimitOrder("1", 100, 150.25)
	raw := string(msg.Raw)

	requiredSubstrings := []string{
		"35=D\x01",   // MsgType
		"54=1\x01",   // Side = Buy
		"38=100\x01", // OrderQty
		"40=2\x01",   // OrdType = Limit
		"59=0\x01",   // TimeInForce = Day
	}
	for _, s := range requiredSubstrings {
		if !strings.Contains(raw, s) {
			t.Errorf("limit order missing %q in raw: %q", s, raw)
		}
	}
}

// TestMarketOrderContainsRequiredTags verifies all mandatory fields for a market order.
func TestMarketOrderContainsRequiredTags(t *testing.T) {
	g := newGen()
	msg := g.MarketOrder("2", 75)
	raw := string(msg.Raw)

	requiredSubstrings := []string{
		"35=D\x01",  // MsgType
		"54=2\x01",  // Side = Sell
		"38=75\x01", // OrderQty
		"40=1\x01",  // OrdType = Market
	}
	for _, s := range requiredSubstrings {
		if !strings.Contains(raw, s) {
			t.Errorf("market order missing %q in raw: %q", s, raw)
		}
	}
}

// TestCancelReplaceContainsRequiredTags verifies all mandatory fields for cancel/replace.
func TestCancelReplaceContainsRequiredTags(t *testing.T) {
	g := newGen()
	orig := "run-abc-bot-1-1"
	msg := g.CancelReplace(orig, "1", 200, 155.0)
	raw := string(msg.Raw)

	requiredSubstrings := []string{
		"35=G\x01",                                    // MsgType
		"\x0141=" + orig + "\x01",                     // OrigClOrdID
		"54=1\x01",                                    // Side
		"38=200\x01",                                  // OrderQty
		"\x0111=" + msg.ClOrdID + "\x01",              // ClOrdID
	}
	for _, s := range requiredSubstrings {
		if !strings.Contains(raw, s) {
			t.Errorf("cancel/replace missing %q in raw: %q", s, raw)
		}
	}
}

// TestClOrdIDFormat verifies the ClOrdID format is <runID>-<botID>-<seqNum>.
func TestClOrdIDFormat(t *testing.T) {
	g := &FIXGenerator{BenchmarkRunID: "run-xyz", BotID: "bot-42"}
	msg := g.LimitOrder("1", 10, 100.0)
	expected := "run-xyz-bot-42-1"
	if msg.ClOrdID != expected {
		t.Fatalf("expected ClOrdID=%q, got %q", expected, msg.ClOrdID)
	}
}
