-- V1__create_telemetry_hypertables.sql
-- Creates TimescaleDB hypertables for raw telemetry samples, rolling-window
-- aggregates, and fill events. Adds compression and retention policies.
--
-- Prerequisites:
--   TimescaleDB 2.x extension installed on the target PostgreSQL instance.
--   Run against the telemetry database (separate from the control-plane DB).

CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE;

-- ---------------------------------------------------------------------------
-- telemetry_samples
-- ---------------------------------------------------------------------------
-- One row per raw latency measurement emitted by a Synthetic Trading Bot or
-- a Sandbox response interceptor. Partitioned by event_time (chunk interval
-- auto-selected by TimescaleDB) with a secondary space partition on
-- benchmark_run_id to ensure all events for one run land in a consistent
-- set of chunks.
--
-- latency_us is a generated column computed from the nanosecond timestamps
-- (recv - send) / 1000 with microsecond resolution. Storing it as STORED
-- avoids re-computation on every read.

CREATE TABLE telemetry_samples (
    benchmark_run_id  UUID        NOT NULL,
    bot_id            UUID        NOT NULL,
    seq_num           BIGINT      NOT NULL,
    protocol          TEXT        NOT NULL,   -- FIX | REST | WS
    send_ts_ns        BIGINT      NOT NULL,   -- nanosecond Unix send timestamp
    recv_ts_ns        BIGINT      NOT NULL,   -- nanosecond Unix receive timestamp
    latency_us        NUMERIC     GENERATED ALWAYS AS
                                  ((recv_ts_ns - send_ts_ns) / 1000.0) STORED,
    request_id        TEXT        NOT NULL,
    error_code        TEXT,                   -- NULL for success
    event_time        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (benchmark_run_id, bot_id, seq_num)
);

SELECT create_hypertable(
    'telemetry_samples',
    'event_time',
    partitioning_column   => 'benchmark_run_id',
    number_partitions     => 16
);

-- ---------------------------------------------------------------------------
-- telemetry_aggregates
-- ---------------------------------------------------------------------------
-- Pre-computed rolling-window metrics written by the Telemetry Ingester every
-- 5 seconds. The Leaderboard Service reads from this table for initial
-- snapshot generation and Redis fallback.
--
-- window_start / window_end define the 60-second rolling window that produced
-- the aggregate.

CREATE TABLE telemetry_aggregates (
    benchmark_run_id  UUID        NOT NULL,
    window_start      TIMESTAMPTZ NOT NULL,
    window_end        TIMESTAMPTZ NOT NULL,
    p50_latency_us    NUMERIC,               -- NULL until at least one sample
    p90_latency_us    NUMERIC,
    p99_latency_us    NUMERIC,
    max_tps           BIGINT,                -- peak TPS within the window
    error_rate        NUMERIC,               -- fraction in [0.0, 1.0]
    sample_count      BIGINT,
    PRIMARY KEY (benchmark_run_id, window_start)
);

SELECT create_hypertable(
    'telemetry_aggregates',
    'window_start'
);

-- ---------------------------------------------------------------------------
-- fill_events
-- ---------------------------------------------------------------------------
-- One row per fill reported by a Sandbox submission. The Telemetry Ingester
-- validates each fill against the Reference Matching Engine and records the
-- outcome in violation_flags and is_correct.
--
-- violation_flags is a TEXT[] that may contain zero or more of:
--   PRICE_PRIORITY_VIOLATION, TIME_PRIORITY_VIOLATION, QUANTITY_MISMATCH

CREATE TABLE fill_events (
    benchmark_run_id  UUID        NOT NULL,
    order_id          TEXT        NOT NULL,
    fill_ts           TIMESTAMPTZ NOT NULL,
    filled_qty        NUMERIC     NOT NULL,
    fill_price        NUMERIC     NOT NULL,
    side              TEXT        NOT NULL,  -- BUY | SELL
    violation_flags   TEXT[],               -- NULL or empty array means correct
    is_correct        BOOLEAN     NOT NULL,
    PRIMARY KEY (benchmark_run_id, order_id, fill_ts)
);

SELECT create_hypertable(
    'fill_events',
    'fill_ts',
    partitioning_column => 'benchmark_run_id',
    number_partitions   => 16
);

-- ---------------------------------------------------------------------------
-- Compression policies
-- ---------------------------------------------------------------------------
-- Compress chunks older than 7 days to reduce storage. Compressed chunks
-- remain queryable but cannot be appended to.

SELECT add_compression_policy('telemetry_samples', INTERVAL '7 days');
SELECT add_compression_policy('fill_events',       INTERVAL '7 days');

-- ---------------------------------------------------------------------------
-- Retention policies
-- ---------------------------------------------------------------------------
-- Automatically drop chunks (and their compressed equivalents) older than
-- 30 days to bound storage growth.

SELECT add_retention_policy('telemetry_samples', INTERVAL '30 days');
SELECT add_retention_policy('fill_events',       INTERVAL '30 days');
