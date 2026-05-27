-- V1__create_contestants.sql
-- Creates the contestants and contestant_tokens tables.
-- Requires: PostgreSQL 14+ with the pgcrypto extension.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ---------------------------------------------------------------------------
-- contestants
-- ---------------------------------------------------------------------------
-- One row per registered hackathon contestant. The UUID primary key is
-- generated server-side using pgcrypto's gen_random_uuid() to avoid any
-- dependency on application-layer UUID generation.

CREATE TABLE contestants (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    handle        TEXT        NOT NULL UNIQUE,   -- alphanumeric display name
    email         TEXT        NOT NULL UNIQUE,   -- for notifications
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- contestant_tokens
-- ---------------------------------------------------------------------------
-- One row per issued API bearer token. The raw token value is NEVER stored;
-- only a SHA-256 hash (BYTEA) is persisted. Token verification by the
-- Submission Engine computes SHA-256(provided_token) and compares against
-- token_hash with a constant-time comparison to prevent timing attacks.
--
-- Tokens may be soft-revoked (revoked = true) without deleting the row so
-- that the audit trail is preserved.

CREATE TABLE contestant_tokens (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    contestant_id UUID        NOT NULL REFERENCES contestants(id)
                              ON DELETE CASCADE,
    token_hash    BYTEA       NOT NULL UNIQUE,   -- SHA-256(raw_token)
    issued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ,                   -- NULL means no expiry
    revoked       BOOLEAN     NOT NULL DEFAULT false
);

-- Index supports the Submission Engine's lookup pattern:
--   SELECT * FROM contestant_tokens WHERE contestant_id = $1 AND revoked = false
CREATE INDEX idx_tokens_contestant ON contestant_tokens(contestant_id);
