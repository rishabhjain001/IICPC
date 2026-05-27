package kafka

import (
	"context"
	"encoding/json"
	"testing"
)

// --- helpers ------------------------------------------------------------------

// stubReader is an in-memory MessageReader that yields a predefined sequence
// of messages then blocks until ctx is cancelled.
type stubReader struct {
	messages []stubMsg
	pos      int
}

type stubMsg struct {
	topic string
	value []byte
}

func (r *stubReader) FetchMessage(ctx context.Context) (topic string, key []byte, value []byte, err error) {
	if r.pos < len(r.messages) {
		m := r.messages[r.pos]
		r.pos++
		return m.topic, nil, m.value, nil
	}
	// No more messages — block until ctx is cancelled.
	<-ctx.Done()
	return "", nil, nil, ctx.Err()
}

func (r *stubReader) CommitOffset(_ context.Context) error { return nil }
func (r *stubReader) Close() error                         { return nil }

// --- Task 11.1 unit tests -----------------------------------------------------

// TestTelemetryEventJSONDeserialization verifies that a raw Kafka message
// containing JSON that matches the Avro field names is correctly decoded into a
// TelemetryEvent.
func TestTelemetryEventJSONDeserialization(t *testing.T) {
	errorCode := "429"
	original := TelemetryEvent{
		SchemaVersion:  1,
		BenchmarkRunID: "run-uuid-001",
		BotID:          "bot-uuid-001",
		SeqNum:         42,
		Protocol:       "FIX",
		SendTsNs:       1_700_000_000_000_000_000,
		RecvTsNs:       1_700_000_000_001_500_000, // 1.5 ms RTT
		RequestID:      "req-uuid-001",
		ErrorCode:      &errorCode,
		PayloadBytes:   []byte("raw-fix-payload"),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal test event: %v", err)
	}

	got, err := deserializeTelemetryEvent(data)
	if err != nil {
		t.Fatalf("deserializeTelemetryEvent returned unexpected error: %v", err)
	}

	if got.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", got.SchemaVersion, original.SchemaVersion)
	}
	if got.BenchmarkRunID != original.BenchmarkRunID {
		t.Errorf("BenchmarkRunID: got %q, want %q", got.BenchmarkRunID, original.BenchmarkRunID)
	}
	if got.BotID != original.BotID {
		t.Errorf("BotID: got %q, want %q", got.BotID, original.BotID)
	}
	if got.SeqNum != original.SeqNum {
		t.Errorf("SeqNum: got %d, want %d", got.SeqNum, original.SeqNum)
	}
	if got.Protocol != original.Protocol {
		t.Errorf("Protocol: got %q, want %q", got.Protocol, original.Protocol)
	}
	if got.SendTsNs != original.SendTsNs {
		t.Errorf("SendTsNs: got %d, want %d", got.SendTsNs, original.SendTsNs)
	}
	if got.RecvTsNs != original.RecvTsNs {
		t.Errorf("RecvTsNs: got %d, want %d", got.RecvTsNs, original.RecvTsNs)
	}
	if got.RequestID != original.RequestID {
		t.Errorf("RequestID: got %q, want %q", got.RequestID, original.RequestID)
	}
	if got.ErrorCode == nil || *got.ErrorCode != errorCode {
		t.Errorf("ErrorCode: got %v, want %q", got.ErrorCode, errorCode)
	}
	if string(got.PayloadBytes) != string(original.PayloadBytes) {
		t.Errorf("PayloadBytes: got %q, want %q", got.PayloadBytes, original.PayloadBytes)
	}
}

// TestTelemetryEventNullErrorCode verifies that a null error_code field is
// correctly decoded as a nil pointer.
func TestTelemetryEventNullErrorCode(t *testing.T) {
	raw := `{"schema_version":1,"benchmark_run_id":"r","bot_id":"b","seq_num":1,"protocol":"REST","send_ts_ns":1000,"recv_ts_ns":2000,"request_id":"req","error_code":null,"payload_bytes":""}`
	got, err := deserializeTelemetryEvent([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ErrorCode != nil {
		t.Errorf("ErrorCode: expected nil, got %v", got.ErrorCode)
	}
}

// TestFillEventJSONDeserialization verifies that a raw Kafka message containing
// JSON matching the Avro fill_event.avsc field names is correctly decoded.
func TestFillEventJSONDeserialization(t *testing.T) {
	original := FillEvent{
		SchemaVersion:  1,
		BenchmarkRunID: "run-uuid-002",
		OrderID:        "order-uuid-001",
		FillTsNs:       1_700_000_001_000_000_000,
		FilledQty:      100,
		FillPrice:      "150.25000000",
		Side:           "BUY",
		ClOrdID:        "cl-ord-001",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal test fill event: %v", err)
	}

	got, err := deserializeFillEvent(data)
	if err != nil {
		t.Fatalf("deserializeFillEvent returned unexpected error: %v", err)
	}

	if got.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", got.SchemaVersion, original.SchemaVersion)
	}
	if got.BenchmarkRunID != original.BenchmarkRunID {
		t.Errorf("BenchmarkRunID: got %q, want %q", got.BenchmarkRunID, original.BenchmarkRunID)
	}
	if got.OrderID != original.OrderID {
		t.Errorf("OrderID: got %q, want %q", got.OrderID, original.OrderID)
	}
	if got.FillTsNs != original.FillTsNs {
		t.Errorf("FillTsNs: got %d, want %d", got.FillTsNs, original.FillTsNs)
	}
	if got.FilledQty != original.FilledQty {
		t.Errorf("FilledQty: got %d, want %d", got.FilledQty, original.FilledQty)
	}
	if got.FillPrice != original.FillPrice {
		t.Errorf("FillPrice: got %q, want %q", got.FillPrice, original.FillPrice)
	}
	if got.Side != original.Side {
		t.Errorf("Side: got %q, want %q", got.Side, original.Side)
	}
	if got.ClOrdID != original.ClOrdID {
		t.Errorf("ClOrdID: got %q, want %q", got.ClOrdID, original.ClOrdID)
	}
}

// TestFillEventInvalidJSON verifies that invalid JSON produces an error.
func TestFillEventInvalidJSON(t *testing.T) {
	_, err := deserializeFillEvent([]byte("not-json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// TestConsumerRoutesEventsToChannels verifies that Run dispatches events to
// the correct output channels based on topic name.
func TestConsumerRoutesEventsToChannels(t *testing.T) {
	rawEvent := TelemetryEvent{
		SchemaVersion:  1,
		BenchmarkRunID: "run-1",
		BotID:          "bot-1",
		SeqNum:         1,
		Protocol:       "WS",
		SendTsNs:       1000,
		RecvTsNs:       2000,
		RequestID:      "req-1",
	}
	fillEvent := FillEvent{
		SchemaVersion:  1,
		BenchmarkRunID: "run-1",
		OrderID:        "ord-1",
		FillTsNs:       3000,
		FilledQty:      10,
		FillPrice:      "100.00",
		Side:           "SELL",
		ClOrdID:        "cl-1",
	}

	rawBytes, _ := json.Marshal(rawEvent)
	fillBytes, _ := json.Marshal(fillEvent)

	reader := &stubReader{
		messages: []stubMsg{
			{topic: "telemetry.raw.dev", value: rawBytes},
			{topic: "telemetry.fills.dev", value: fillBytes},
		},
	}

	rawCh := make(chan TelemetryEvent, 1)
	fillCh := make(chan FillEvent, 1)

	consumer := &Consumer{
		Brokers: []string{"localhost:9092"},
		GroupID: "test-group",
		Env:     "dev",
		RawCh:   rawCh,
		FillCh:  fillCh,
		Reader:  reader,
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- consumer.Run(ctx)
	}()

	// Receive both events.
	gotRaw := <-rawCh
	gotFill := <-fillCh

	// Cancel context to stop the consumer loop.
	cancel()
	<-done

	if gotRaw.BenchmarkRunID != rawEvent.BenchmarkRunID {
		t.Errorf("raw event BenchmarkRunID: got %q, want %q", gotRaw.BenchmarkRunID, rawEvent.BenchmarkRunID)
	}
	if gotFill.OrderID != fillEvent.OrderID {
		t.Errorf("fill event OrderID: got %q, want %q", gotFill.OrderID, fillEvent.OrderID)
	}
}
