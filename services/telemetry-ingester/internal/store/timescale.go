package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TimescaleStore handles persistence to TimescaleDB hypertables.
// All writes target the hypertables defined in
// migrations/timescaledb/V1__create_telemetry_hypertables.sql.
//
// The 500 ms write-latency budget (Requirement 7.4) is enforced by the caller
// via context deadlines; this struct does not add its own timeouts.
type TimescaleStore struct {
	// Pool is the pgxpool connection pool to the TimescaleDB instance.
	Pool *pgxpool.Pool
}

// InsertTelemetrySample inserts a raw latency sample into the
// telemetry_samples hypertable.  The latency_us column is a generated column
// computed from send_ts_ns and recv_ts_ns, so it is not supplied directly.
//
// The caller should wrap ctx with a 500 ms deadline to satisfy Requirement 7.4.
func (s *TimescaleStore) InsertTelemetrySample(ctx context.Context, event TelemetrySample) error {
	const query = `
		INSERT INTO telemetry_samples
			(benchmark_run_id, bot_id, seq_num, protocol,
			 send_ts_ns, recv_ts_ns, request_id, error_code, event_time)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (benchmark_run_id, bot_id, seq_num) DO NOTHING`

	_, err := s.Pool.Exec(ctx, query,
		event.BenchmarkRunID,
		event.BotID,
		event.SeqNum,
		event.Protocol,
		event.SendTsNs,
		event.RecvTsNs,
		event.RequestID,
		event.ErrorCode,
		event.EventTime,
	)
	if err != nil {
		return fmt.Errorf("InsertTelemetrySample: %w", err)
	}
	return nil
}

// InsertAggregate inserts a rolling-window aggregate row into the
// telemetry_aggregates hypertable.
//
// The caller should wrap ctx with a 500 ms deadline to satisfy Requirement 7.4.
func (s *TimescaleStore) InsertAggregate(ctx context.Context, agg TelemetryAggregate) error {
	const query = `
		INSERT INTO telemetry_aggregates
			(benchmark_run_id, window_start, window_end,
			 p50_latency_us, p90_latency_us, p99_latency_us,
			 max_tps, error_rate, sample_count)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (benchmark_run_id, window_start) DO UPDATE
			SET window_end      = EXCLUDED.window_end,
			    p50_latency_us  = EXCLUDED.p50_latency_us,
			    p90_latency_us  = EXCLUDED.p90_latency_us,
			    p99_latency_us  = EXCLUDED.p99_latency_us,
			    max_tps         = EXCLUDED.max_tps,
			    error_rate      = EXCLUDED.error_rate,
			    sample_count    = EXCLUDED.sample_count`

	_, err := s.Pool.Exec(ctx, query,
		agg.BenchmarkRunID,
		agg.WindowStart,
		agg.WindowEnd,
		agg.P50LatencyUs,
		agg.P90LatencyUs,
		agg.P99LatencyUs,
		agg.MaxTPS,
		agg.ErrorRate,
		agg.SampleCount,
	)
	if err != nil {
		return fmt.Errorf("InsertAggregate: %w", err)
	}
	return nil
}

// InsertFillEvent inserts a fill event with correctness flags into the
// fill_events hypertable.
//
// ViolationFlags is stored as a PostgreSQL TEXT[] array.  An empty slice
// (no violations) is stored as an empty array, not NULL.
//
// The caller should wrap ctx with a 500 ms deadline to satisfy Requirement 7.4.
func (s *TimescaleStore) InsertFillEvent(ctx context.Context, fill FillRecord) error {
	const query = `
		INSERT INTO fill_events
			(benchmark_run_id, order_id, fill_ts, filled_qty,
			 fill_price, side, violation_flags, is_correct)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (benchmark_run_id, order_id, fill_ts) DO UPDATE
			SET violation_flags = EXCLUDED.violation_flags,
			    is_correct       = EXCLUDED.is_correct`

	_, err := s.Pool.Exec(ctx, query,
		fill.BenchmarkRunID,
		fill.OrderID,
		fill.FillTs,
		fill.FilledQty,
		fill.FillPrice,
		fill.Side,
		fill.ViolationFlags,
		fill.IsCorrect,
	)
	if err != nil {
		return fmt.Errorf("InsertFillEvent: %w", err)
	}
	return nil
}

// RecordError records an error event (HTTP 4xx/5xx, FIX reject, or WebSocket
// close frame) to the telemetry_samples hypertable by inserting a synthetic
// sample whose error_code field is set (Requirement 7.6).
//
// The sent and received timestamps are both set to ts (zero-latency record),
// which produces a latency_us of 0 in the generated column.  This is
// intentional: error events are not latency samples.
func (s *TimescaleStore) RecordError(ctx context.Context, runID, requestID, errorCode string, ts time.Time) error {
	ec := errorCode
	errSample := TelemetrySample{
		BenchmarkRunID: runID,
		BotID:          "error-recorder", // sentinel bot ID for error records
		SeqNum:         ts.UnixNano(),    // unique key based on ns timestamp
		Protocol:       "ERROR",
		SendTsNs:       ts.UnixNano(),
		RecvTsNs:       ts.UnixNano(),
		RequestID:      requestID,
		ErrorCode:      &ec,
		EventTime:      ts,
	}
	return s.InsertTelemetrySample(ctx, errSample)
}
