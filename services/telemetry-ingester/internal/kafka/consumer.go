// Package kafka provides a channel-based Kafka consumer interface for the
// Telemetry Ingester. In production, the Run method would be backed by the
// librdkafka-based confluent-kafka-go client (or Redpanda's compatible API)
// and would use the Confluent Schema Registry for proper Avro deserialization.
//
// For local development and testing, messages are deserialized from JSON with
// field names matching the Avro schema in schemas/telemetry/.  The Consumer
// struct is designed so a test double can substitute the underlying transport
// while keeping the same channel-based API contract.
package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// TelemetryEvent mirrors the Avro schema in schemas/telemetry/telemetry_event.avsc.
// Field names use snake_case JSON tags that match the Avro field names so that
// the JSON dev-mode fallback and a future Avro codec share the same struct.
type TelemetryEvent struct {
	// SchemaVersion carries the Avro schema_version field for BACKWARD-compatible evolution.
	SchemaVersion int32 `json:"schema_version"`
	// BenchmarkRunID is the UUID of the Benchmark Run this event belongs to.
	// It is used as the Kafka partition key.
	BenchmarkRunID string `json:"benchmark_run_id"`
	// BotID is the UUID of the Synthetic Trading Bot that sent the original message.
	BotID string `json:"bot_id"`
	// SeqNum is a monotonically increasing per-bot-session sequence number
	// (resets to 1 at session start). Used for deduplication.
	SeqNum int64 `json:"seq_num"`
	// Protocol is one of "FIX", "REST", or "WS".
	Protocol string `json:"protocol"`
	// SendTsNs is the nanosecond-resolution Unix timestamp at message send.
	SendTsNs int64 `json:"send_ts_ns"`
	// RecvTsNs is the nanosecond-resolution Unix timestamp at response receipt.
	RecvTsNs int64 `json:"recv_ts_ns"`
	// RequestID is a globally unique request ID within the Benchmark Run.
	RequestID string `json:"request_id"`
	// ErrorCode is optional; nil for successful messages.
	ErrorCode *string `json:"error_code"`
	// PayloadBytes holds the raw serialised message payload (truncated at 4 KB).
	PayloadBytes []byte `json:"payload_bytes"`
}

// FillEvent mirrors the Avro schema in schemas/telemetry/fill_event.avsc.
// fill_price is represented as a decimal string because the Avro schema uses a
// bytes/decimal logical type; string avoids floating-point precision loss.
type FillEvent struct {
	// SchemaVersion carries the Avro schema_version field.
	SchemaVersion int32 `json:"schema_version"`
	// BenchmarkRunID is the UUID of the Benchmark Run.
	BenchmarkRunID string `json:"benchmark_run_id"`
	// OrderID is the platform-assigned order identifier that was filled.
	OrderID string `json:"order_id"`
	// FillTsNs is the nanosecond-resolution Unix timestamp of the fill.
	FillTsNs int64 `json:"fill_ts_ns"`
	// FilledQty is the number of shares / lots filled.
	FilledQty int64 `json:"filled_qty"`
	// FillPrice is the execution price as a decimal string (e.g. "123.45000000").
	FillPrice string `json:"fill_price"`
	// Side is "BUY" or "SELL" for the aggressive (matched) order.
	Side string `json:"side"`
	// ClOrdID is the client order ID used for end-to-end correlation.
	ClOrdID string `json:"cl_ord_id"`
}

// MessageReader is the minimal interface the Consumer uses to read raw Kafka
// messages. Implementations can be the real librdkafka client (production) or
// a channel-based stub (tests / local dev).
//
// Production note: replace with confluent-kafka-go's *kafka.Consumer, which
// provides Fetch(), Commit(), and Pause()/Resume() over the librdkafka C
// library. The interface lets tests avoid the CGo dependency entirely.
type MessageReader interface {
	// FetchMessage blocks until a message is available or ctx is cancelled.
	// Returns (topic, key, value, err).
	FetchMessage(ctx context.Context) (topic string, key []byte, value []byte, err error)
	// CommitOffset acknowledges the last fetched message.
	CommitOffset(ctx context.Context) error
	// Close releases resources.
	Close() error
}

// Consumer reads from Kafka topics and routes events to handler channels.
// It uses a single MessageReader so tests can inject a stub without any
// real Kafka / librdkafka dependency.
//
// Production deployment:
//   - Set Reader to a confluent-kafka-go consumer initialised with the
//     bootstrap servers, group ID, and Confluent Schema Registry URL.
//   - Replace deserializeTelemetryEvent / deserializeFillEvent with Avro
//     binary decoding using the schema registry client.
//   - Topics consumed: telemetry.raw.{Env} and telemetry.fills.{Env}.
type Consumer struct {
	// Brokers is the list of Kafka bootstrap server addresses.
	// Informational only when Reader is provided externally.
	Brokers []string
	// GroupID is the Kafka consumer group identifier.
	GroupID string
	// Env is the deployment environment suffix, e.g. "dev" or "prod".
	Env string
	// RawCh receives deserialized TelemetryEvents from telemetry.raw.{Env}.
	RawCh chan<- TelemetryEvent
	// FillCh receives deserialized FillEvents from telemetry.fills.{Env}.
	FillCh chan<- FillEvent
	// Reader is the underlying message source. Must be set before calling Run.
	// In production: confluent_kafka.NewConsumer(...). In tests: a stub.
	Reader MessageReader
	// Logger is an optional structured logger; defaults to a no-op logger.
	Logger *zap.Logger
}

func (c *Consumer) logger() *zap.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return zap.NewNop()
}

// rawTopic returns the topic name for raw telemetry events.
func (c *Consumer) rawTopic() string {
	return fmt.Sprintf("telemetry.raw.%s", c.Env)
}

// fillTopic returns the topic name for fill events.
func (c *Consumer) fillTopic() string {
	return fmt.Sprintf("telemetry.fills.%s", c.Env)
}

// Run starts consuming from telemetry.raw.{Env} and telemetry.fills.{Env}.
// It blocks until ctx is cancelled. All events are dispatched synchronously
// to RawCh / FillCh; the caller is responsible for draining those channels
// promptly to avoid blocking the consumer loop.
//
// Deserialisation strategy (dev/test mode):
//   Messages are expected to be UTF-8 JSON with field names matching the
//   Avro schema.  A production deployment replaces this with proper Avro
//   binary decoding via the Confluent Schema Registry client.
//
// At-least-once delivery is guaranteed by committing offsets only after a
// message has been dispatched to the appropriate channel (Requirement 7.8).
func (c *Consumer) Run(ctx context.Context) error {
	log := c.logger()
	log.Info("consumer starting",
		zap.Strings("brokers", c.Brokers),
		zap.String("group_id", c.GroupID),
		zap.String("env", c.Env),
		zap.String("raw_topic", c.rawTopic()),
		zap.String("fill_topic", c.fillTopic()),
	)

	for {
		topic, _, value, err := c.Reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// Context cancelled — clean shutdown.
				log.Info("consumer stopping due to context cancellation")
				return ctx.Err()
			}
			log.Error("fetch error", zap.Error(err))
			// Brief back-off before retrying to avoid tight loops on
			// transient network errors.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}

		switch topic {
		case c.rawTopic():
			event, deserErr := deserializeTelemetryEvent(value)
			if deserErr != nil {
				log.Error("failed to deserialize telemetry event",
					zap.Error(deserErr),
					zap.String("topic", topic),
				)
			} else {
				// Blocking send; caller must keep RawCh drained.
				select {
				case c.RawCh <- event:
				case <-ctx.Done():
					return ctx.Err()
				}
			}

		case c.fillTopic():
			fill, deserErr := deserializeFillEvent(value)
			if deserErr != nil {
				log.Error("failed to deserialize fill event",
					zap.Error(deserErr),
					zap.String("topic", topic),
				)
			} else {
				select {
				case c.FillCh <- fill:
				case <-ctx.Done():
					return ctx.Err()
				}
			}

		default:
			log.Warn("received message on unexpected topic", zap.String("topic", topic))
		}

		// Commit offset after dispatching to channel (at-least-once semantics).
		if commitErr := c.Reader.CommitOffset(ctx); commitErr != nil {
			log.Error("offset commit failed", zap.Error(commitErr))
		}
	}
}

// deserializeTelemetryEvent decodes a JSON payload into a TelemetryEvent.
// Production note: replace with Avro binary deserialization using the
// Confluent Schema Registry client (goavro or schema-registry-client-go).
func deserializeTelemetryEvent(data []byte) (TelemetryEvent, error) {
	var e TelemetryEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return TelemetryEvent{}, fmt.Errorf("deserializeTelemetryEvent: %w", err)
	}
	return e, nil
}

// deserializeFillEvent decodes a JSON payload into a FillEvent.
// Production note: same as above — replace with Avro deserialization.
func deserializeFillEvent(data []byte) (FillEvent, error) {
	var f FillEvent
	if err := json.Unmarshal(data, &f); err != nil {
		return FillEvent{}, fmt.Errorf("deserializeFillEvent: %w", err)
	}
	return f, nil
}
