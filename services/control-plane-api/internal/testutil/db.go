// Package testutil provides shared test helpers for the control-plane-api
// service. Tests that require a live database are skipped when DATABASE_URL
// is not set, so they can be compiled and validated without a running
// PostgreSQL instance.
package testutil

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MustPool returns a *pgxpool.Pool connected to the database specified by the
// DATABASE_URL environment variable. If DATABASE_URL is not set the calling
// test is skipped, not failed, so the suite still compiles and runs cleanly in
// environments without a database.
func MustPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping database integration test")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("testutil.MustPool: connect to database: %v", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("testutil.MustPool: ping database: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}
