// Package main is the entry point for the Control Plane API service.
//
// It starts a gRPC server with bearer-token authentication interceptors,
// exposes a Prometheus /metrics HTTP endpoint, and wires together the
// ContestantService, SubmissionService, and BenchmarkService handlers.
package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/iicpc/dbhp/control-plane-api/internal/benchmark"
	"github.com/iicpc/dbhp/control-plane-api/internal/contestant"
	"github.com/iicpc/dbhp/control-plane-api/internal/metrics"
	"github.com/iicpc/dbhp/control-plane-api/internal/middleware"
	"github.com/iicpc/dbhp/control-plane-api/internal/submission"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log, _ := zap.NewProduction()
	defer log.Sync() //nolint:errcheck

	// --- Configuration from environment ---
	grpcAddr := envOrDefault("GRPC_ADDR", ":9090")
	metricsAddr := envOrDefault("METRICS_ADDR", ":9091")
	databaseURL := mustEnv(log, "DATABASE_URL")
	bearerToken := mustEnv(log, "INTERNAL_BEARER_TOKEN")

	// --- PostgreSQL connection pool ---
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatal("connect to database", zap.Error(err))
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatal("ping database", zap.Error(err))
	}
	log.Info("database connected")

	// --- Services ---
	contestantSvc := contestant.NewContestantService(pool)
	submissionSvc := submission.NewSubmissionService(pool)
	benchmarkSvc := benchmark.NewBenchmarkService(pool, log)

	// Keep the compiler happy: these services are ready for gRPC handler
	// registration once proto-generated server interfaces are available.
	_ = contestantSvc
	_ = submissionSvc
	_ = benchmarkSvc

	// --- Prometheus metrics ---
	reg := metrics.NewRegistry()
	m := metrics.RegisterMetrics(reg)
	_ = m // available for use in gRPC handlers

	// --- gRPC server with bearer-token interceptors ---
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			middleware.BearerTokenInterceptor(bearerToken),
		),
		grpc.ChainStreamInterceptor(
			middleware.BearerTokenStreamInterceptor(bearerToken),
		),
	)

	// --- HTTP server for /metrics ---
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	metricsServer := &http.Server{
		Addr:         metricsAddr,
		Handler:      metricsMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Start the /metrics HTTP server in the background.
	go func() {
		log.Info("metrics server listening", zap.String("addr", metricsAddr))
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server error", zap.Error(err))
		}
	}()

	// Start the gRPC server.
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatal("gRPC listen", zap.Error(err))
	}
	log.Info("gRPC server listening", zap.String("addr", grpcAddr))

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Error("gRPC server error", zap.Error(err))
		}
	}()

	// --- Graceful shutdown ---
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Info("shutting down")
	grpcServer.GracefulStop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("metrics server shutdown error", zap.Error(err))
	}

	log.Info("shutdown complete")
}

// envOrDefault returns the value of the named environment variable, or def if
// the variable is unset or empty.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// mustEnv returns the value of the named environment variable, or exits with a
// fatal log message if the variable is unset or empty.
func mustEnv(log *zap.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatal("required environment variable not set", zap.String("var", key))
	}
	return v
}
