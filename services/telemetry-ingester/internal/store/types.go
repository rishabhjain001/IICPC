// Package store provides persistence types and adapters for TimescaleDB
// hypertables used by the Telemetry Ingester.
package store

import "time"

// TelemetrySample represents a single raw latency measurement to be persisted
// in the telemetry_samples hypertable (Requirement 7.4).
type TelemetrySample struct {
	BenchmarkRunID string
	BotID          string
	SeqNum         int64
	Protocol       string // "FIX" | "REST" | "WS"
	SendTsNs       int64
	RecvTsNs       int64
	RequestID      string
	ErrorCode      *string
	// EventTime is the wall-clock time used as the hypertable partition key.
	EventTime time.Time
}

// TelemetryAggregate represents a pre-computed rolling-window aggregate row to
// be persisted in the telemetry_aggregates hypertable (Requirement 7.4).
type TelemetryAggregate struct {
	BenchmarkRunID string
	WindowStart    time.Time
	WindowEnd      time.Time
	P50LatencyUs   float64
	P90LatencyUs   float64
	P99LatencyUs   float64
	MaxTPS         int64
	ErrorRate      float64
	SampleCount    int64
}

// FillRecord represents a single fill event with correctness flags to be
// persisted in the fill_events hypertable (Requirement 7.4, 8.1–8.5).
type FillRecord struct {
	BenchmarkRunID string
	OrderID        string
	FillTs         time.Time
	FilledQty      int64
	FillPrice      string // decimal string
	Side           string // "BUY" | "SELL"
	ViolationFlags []string
	IsCorrect      bool
}
