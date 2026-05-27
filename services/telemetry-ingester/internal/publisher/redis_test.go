package publisher

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/redis/go-redis/v9"
)

// --- stub Publisher ----------------------------------------------------------

// capturedPublish holds the arguments of one Publish call.
type capturedPublish struct {
	channel string
	payload string
}

// fakePublisher is a test double for the Publisher interface.
// It records all Publish calls and returns a configurable error.
type fakePublisher struct {
	published []capturedPublish
	returnErr error
}

// Publish records the call and returns an IntCmd pre-set with returnErr.
func (f *fakePublisher) Publish(_ context.Context, channel string, message interface{}) *redis.IntCmd {
	var payload string
	switch v := message.(type) {
	case string:
		payload = v
	case []byte:
		payload = string(v)
	default:
		payload = ""
	}
	f.published = append(f.published, capturedPublish{channel: channel, payload: payload})
	cmd := redis.NewIntCmd(context.Background())
	if f.returnErr != nil {
		cmd.SetErr(f.returnErr)
	}
	return cmd
}

// Ensure fakePublisher satisfies the Publisher interface at compile time.
var _ Publisher = (*fakePublisher)(nil)

// --- tests -------------------------------------------------------------------

// TestPublishSerializesJSONCorrectly verifies that Publish marshals an
// AggregatedMetrics value into the expected JSON envelope and sends it to
// the correct channel key.
func TestPublishSerializesJSONCorrectly(t *testing.T) {
	fake := &fakePublisher{}
	pub := &MetricsPublisher{RDB: fake}

	runID := "bench-run-001"
	metrics := AggregatedMetrics{
		P50LatencyUs: 100.5,
		P90LatencyUs: 250.0,
		P99LatencyUs: 800.0,
		MaxTPS:       45000,
		ErrorRate:    0.02,
	}

	if err := pub.Publish(context.Background(), runID, metrics); err != nil {
		t.Fatalf("Publish returned unexpected error: %v", err)
	}

	if len(fake.published) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(fake.published))
	}

	got := fake.published[0]
	wantChannel := "pubsub:metrics:bench-run-001"
	if got.channel != wantChannel {
		t.Errorf("channel: got %q, want %q", got.channel, wantChannel)
	}

	var decoded publishedMessage
	if err := json.Unmarshal([]byte(got.payload), &decoded); err != nil {
		t.Fatalf("could not decode published payload: %v\npayload: %s", err, got.payload)
	}

	if decoded.BenchmarkRunID != runID {
		t.Errorf("BenchmarkRunID: got %q, want %q", decoded.BenchmarkRunID, runID)
	}
	if decoded.P50LatencyUs != metrics.P50LatencyUs {
		t.Errorf("P50LatencyUs: got %v, want %v", decoded.P50LatencyUs, metrics.P50LatencyUs)
	}
	if decoded.P90LatencyUs != metrics.P90LatencyUs {
		t.Errorf("P90LatencyUs: got %v, want %v", decoded.P90LatencyUs, metrics.P90LatencyUs)
	}
	if decoded.P99LatencyUs != metrics.P99LatencyUs {
		t.Errorf("P99LatencyUs: got %v, want %v", decoded.P99LatencyUs, metrics.P99LatencyUs)
	}
	if decoded.MaxTPS != metrics.MaxTPS {
		t.Errorf("MaxTPS: got %d, want %d", decoded.MaxTPS, metrics.MaxTPS)
	}
	if decoded.ErrorRate != metrics.ErrorRate {
		t.Errorf("ErrorRate: got %v, want %v", decoded.ErrorRate, metrics.ErrorRate)
	}
}

// TestPublishIncludesErrorRate verifies that a non-zero ErrorRate is preserved
// in the published message (a regression guard for the error-rate field).
func TestPublishIncludesErrorRate(t *testing.T) {
	fake := &fakePublisher{}
	pub := &MetricsPublisher{RDB: fake}

	metrics := AggregatedMetrics{
		P50LatencyUs: 50,
		P90LatencyUs: 100,
		P99LatencyUs: 200,
		MaxTPS:       1000,
		ErrorRate:    0.15, // 15% error rate
	}

	if err := pub.Publish(context.Background(), "run-x", metrics); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	payload := fake.published[0].payload
	var decoded publishedMessage
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ErrorRate != 0.15 {
		t.Errorf("ErrorRate: got %v, want 0.15", decoded.ErrorRate)
	}
}

// TestPublishZeroMetrics verifies that zero-valued metrics are published
// correctly (important when no events have been received yet).
func TestPublishZeroMetrics(t *testing.T) {
	fake := &fakePublisher{}
	pub := &MetricsPublisher{RDB: fake}

	if err := pub.Publish(context.Background(), "run-zero", AggregatedMetrics{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if len(fake.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(fake.published))
	}

	var decoded publishedMessage
	if err := json.Unmarshal([]byte(fake.published[0].payload), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.BenchmarkRunID != "run-zero" {
		t.Errorf("BenchmarkRunID: got %q, want %q", decoded.BenchmarkRunID, "run-zero")
	}
	if decoded.MaxTPS != 0 {
		t.Errorf("MaxTPS: got %d, want 0", decoded.MaxTPS)
	}
}
