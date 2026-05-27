-- V2__create_submissions.sql
-- Creates the submissions and submission_rate_limits tables.
-- Depends on: V1__create_contestants.sql (contestants table).

-- ---------------------------------------------------------------------------
-- submissions
-- ---------------------------------------------------------------------------
-- One row per uploaded artifact. Updated in-place as the Build Pipeline
-- progresses through UPLOADED → BUILT / BUILD_FAILED / BUILD_TIMEOUT /
-- BUILD_PUBLISH_FAILED / BUILD_INFRASTRUCTURE_ERROR.
--
-- artifact_type values (enforced by CHECK constraint):
--   ELF_BINARY       – pre-compiled Linux ELF executable
--   SOURCE_ARCHIVE   – .tar.gz or .zip with root-level Makefile/Cargo.toml/go.mod

CREATE TABLE submissions (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    contestant_id   UUID        NOT NULL REFERENCES contestants(id)
                                ON DELETE RESTRICT,
    artifact_type   TEXT        NOT NULL
                                CHECK (artifact_type IN ('ELF_BINARY', 'SOURCE_ARCHIVE')),
    artifact_size   BIGINT      NOT NULL,                    -- bytes
    checksum_sha256 BYTEA       NOT NULL,                    -- 32-byte SHA-256 digest
    status          TEXT        NOT NULL DEFAULT 'UPLOADED', -- see shared-go/types/status.go
    artifact_uri    TEXT,                                    -- Artifact Registry path
    image_digest    TEXT,                                    -- OCI image sha256:... digest
    build_log_uri   TEXT,                                    -- Artifact Registry log path
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Supports "list submissions for contestant" queries from the Control Plane.
CREATE INDEX idx_submissions_contestant ON submissions(contestant_id);

-- ---------------------------------------------------------------------------
-- submission_rate_limits
-- ---------------------------------------------------------------------------
-- Append-only log of accepted uploads. The Submission Engine uses a rolling
-- 60-minute window over this table (or the equivalent Redis sorted set) to
-- enforce the 5-per-60-min rate limit (Requirement 1.6).
--
-- PRIMARY KEY (contestant_id, upload_time) prevents double-inserts at the
-- same nanosecond timestamp, which is practically impossible but correct.

CREATE TABLE submission_rate_limits (
    contestant_id UUID        NOT NULL REFERENCES contestants(id)
                              ON DELETE CASCADE,
    upload_time   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (contestant_id, upload_time)
);
