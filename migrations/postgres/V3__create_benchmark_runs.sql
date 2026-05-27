-- V3__create_benchmark_runs.sql
-- Creates the benchmark_runs and benchmark_run_audit tables.
-- Depends on: V1 (contestants), V2 (submissions).

-- ---------------------------------------------------------------------------
-- benchmark_runs
-- ---------------------------------------------------------------------------
-- Central state table for every Benchmark Run. The Control Plane API and the
-- Leaderboard Service are the primary readers/writers.
--
-- Status lifecycle (see design.md state machine):
--   QUEUED → BUILDING → DEPLOYING → RUNNING → COLLECTING → SCORING → COMPLETE
--   Terminal failures: BUILD_FAILED, BUILD_TIMEOUT, SANDBOX_CRASH,
--   NETWORK_SETUP_FAILED, NO_ENDPOINTS, PROVISIONING_TIMEOUT,
--   INSUFFICIENT_CAPACITY, COLLECTION_TIMEOUT, FAILED
--
-- Score columns use NUMERIC(7,6) — seven total digits with six decimal places
-- gives a range of [0.000000, 1.000000] with sub-microsecond precision.

CREATE TABLE benchmark_runs (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    contestant_id   UUID         NOT NULL REFERENCES contestants(id)
                                 ON DELETE RESTRICT,
    submission_id   UUID         NOT NULL REFERENCES submissions(id)
                                 ON DELETE RESTRICT,
    status          TEXT         NOT NULL DEFAULT 'QUEUED',
    bot_count       INT          NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,            -- set when status → BUILDING
    completed_at    TIMESTAMPTZ,            -- set when status → any terminal
    composite_score NUMERIC(7,6),           -- null until SCORING complete
    speed_score     NUMERIC(7,6),
    stability_score NUMERIC(7,6),
    accuracy_score  NUMERIC(7,6)
);

-- Supports "runs for contestant" queries (Control Plane concurrent-run check).
CREATE INDEX idx_runs_contestant ON benchmark_runs(contestant_id);

-- Supports "runs by status" queries (scheduler, cleanup jobs).
CREATE INDEX idx_runs_status ON benchmark_runs(status);

-- ---------------------------------------------------------------------------
-- benchmark_run_audit
-- ---------------------------------------------------------------------------
-- Append-only audit trail. Every state transition (including terminal failure
-- states) MUST produce a row here before the in-memory state is updated.
-- This survives process restart because it is written in the same DB
-- transaction as the benchmark_runs status UPDATE (Requirement 11.3).
--
-- actor_identity is the gRPC caller identity (contestant_id, service name,
-- or "scheduler") for traceability.

CREATE TABLE benchmark_run_audit (
    id               BIGSERIAL   PRIMARY KEY,
    benchmark_run_id UUID        NOT NULL REFERENCES benchmark_runs(id)
                                 ON DELETE CASCADE,
    prior_state      TEXT        NOT NULL,
    new_state        TEXT        NOT NULL,
    actor_identity   TEXT        NOT NULL,
    failure_reason   TEXT,                  -- populated on terminal failure states
    recorded_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Supports "audit trail for a specific run" queries.
CREATE INDEX idx_audit_run ON benchmark_run_audit(benchmark_run_id);
