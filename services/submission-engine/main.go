// Package main is the entry point for the Submission Engine service.
// It reads TLS and database configuration from environment variables,
// initialises a pgx connection pool, registers HTTP routes, and starts
// an HTTPS server.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/iicpc/dbhp/submission-engine/internal/config"
	"github.com/iicpc/dbhp/submission-engine/internal/db"
	"github.com/iicpc/dbhp/submission-engine/internal/handler"
	"github.com/iicpc/dbhp/submission-engine/internal/middleware"
	"github.com/iicpc/dbhp/submission-engine/internal/ratelimit"
	"github.com/iicpc/dbhp/submission-engine/internal/registry"
	"github.com/iicpc/dbhp/submission-engine/internal/store"
)

func main() {
	// Structured logger — writes JSON to stdout.
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialise logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	cfg := config.Load()

	// Validate required configuration.
	if cfg.DatabaseURL == "" {
		logger.Fatal("DATABASE_URL is required")
	}
	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		logger.Fatal("TLS_CERT_FILE and TLS_KEY_FILE are required")
	}

	// Initialise PostgreSQL connection pool.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatal("failed to connect to PostgreSQL", zap.Error(err))
	}
	defer pool.Close()

	queries := db.NewQueries(pool)

	// Initialise Redis client for rate limiting.
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		// Fall back to a default address so the service can still start in
		// environments where REDIS_URL is not yet set (e.g. local dev without
		// Redis).  A real deployment must provide a valid REDIS_URL.
		logger.Warn("failed to parse REDIS_URL; falling back to localhost:6379",
			zap.String("redis_url", cfg.RedisURL),
			zap.Error(err),
		)
		redisOpts = &redis.Options{Addr: "localhost:6379"}
	}
	rdb := redis.NewClient(redisOpts)
	defer func() { _ = rdb.Close() }()

	rateLimiter := ratelimit.New(rdb)

	// Initialise the Artifact Registry client.
	// FSClient is used when ARTIFACT_REGISTRY_DIR is set; otherwise HTTPClient.
	var reg registry.Client
	if cfg.ArtifactRegistryDir != "" {
		logger.Info("using filesystem artifact registry",
			zap.String("dir", cfg.ArtifactRegistryDir),
		)
		reg = registry.NewFSClient(cfg.ArtifactRegistryDir)
	} else {
		logger.Info("using OCI HTTP artifact registry",
			zap.String("url", cfg.ArtifactRegistryURL),
		)
		reg = registry.NewHTTPClient(cfg.ArtifactRegistryURL)
	}

	// Initialise the submission store for status queries.
	submissionStore := store.New(pool)

	// Build the HTTP mux.
	mux := http.NewServeMux()

	// Health probe — no auth required.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// POST /v1/submissions — auth middleware wraps the upload handler.
	authMiddleware := middleware.Auth(queries, logger)
	mux.Handle("POST /v1/submissions", authMiddleware(handler.Upload(logger, rateLimiter, reg, queries)))

	// GET /v1/submissions/{submission_id}/status — auth middleware also protects
	// this endpoint (Requirement 2.4 / 2.6: authenticated status polling).
	mux.Handle("GET /v1/submissions/{submission_id}/status",
		authMiddleware(handler.Status(logger, submissionStore)),
	)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown on SIGINT / SIGTERM.
	idleConnsClosed := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutdown signal received")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Error("graceful shutdown error", zap.Error(err))
		}
		close(idleConnsClosed)
	}()

	logger.Info("submission-engine starting",
		zap.String("addr", srv.Addr),
		zap.String("tls_cert", cfg.TLSCertFile),
	)

	if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && err != http.ErrServerClosed {
		logger.Fatal("ListenAndServeTLS", zap.Error(err))
	}

	<-idleConnsClosed
	logger.Info("submission-engine stopped")
}
