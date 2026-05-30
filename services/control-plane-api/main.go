package main

import (
	"context"
	"encoding/json"
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
	controlv1 "github.com/iicpc/dbhp/control-plane-api/generated/control/v1"
	"github.com/iicpc/dbhp/control-plane-api/internal/metrics"
	"github.com/iicpc/dbhp/control-plane-api/internal/middleware"
	"github.com/iicpc/dbhp/control-plane-api/internal/submission"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log, _ := zap.NewProduction()
	defer log.Sync() //nolint:errcheck

	grpcAddr := envOrDefault("GRPC_ADDR", ":9090")
	metricsAddr := envOrDefault("METRICS_ADDR", ":9091")
	databaseURL := mustEnv(log, "DATABASE_URL")
	bearerToken := mustEnv(log, "INTERNAL_BEARER_TOKEN")

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

	contestantSvc := contestant.NewContestantService(pool)
	submissionSvc := submission.NewSubmissionService(pool)
	benchmarkSvc := benchmark.NewBenchmarkService(pool, log)

	reg := metrics.NewRegistry()
	m := metrics.RegisterMetrics(reg)
	_ = m

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(middleware.BearerTokenInterceptor(bearerToken)),
		grpc.ChainStreamInterceptor(middleware.BearerTokenStreamInterceptor(bearerToken)),
	)

	// REST API mux — accessible at :9091
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	// POST /api/contestants — register a contestant
	mux.HandleFunc("/api/contestants", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Handle string `json:"handle"`
			Email  string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		c, err := contestantSvc.RegisterContestant(r.Context(), req.Handle, req.Email)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"contestant_id": c})
	})

	// POST /api/contestants/{id}/tokens — issue a token
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ContestantID string `json:"contestant_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token, err := contestantSvc.IssueToken(r.Context(), req.ContestantID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"token": token})
	})

	// GET /api/submissions/{id}/status
	mux.HandleFunc("/api/submissions/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/api/submissions/"):]
		if id == "" {
			http.Error(w, "missing submission id", http.StatusBadRequest)
			return
		}
		status, err := submissionSvc.GetSubmissionStatus(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

	// POST /api/runs — create a benchmark run
	mux.HandleFunc("/api/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req controlv1.CreateRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		run, err := benchmarkSvc.CreateRun(r.Context(), &req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(run)
	})

	// GET /api/runs/{id}
	mux.HandleFunc("/api/runs/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/api/runs/"):]
		if id == "" {
			http.Error(w, "missing run id", http.StatusBadRequest)
			return
		}
		run, err := benchmarkSvc.GetRun(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(run)
	})

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	apiServer := &http.Server{
		Addr:         metricsAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("API+metrics server listening", zap.String("addr", metricsAddr))
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("API server error", zap.Error(err))
		}
	}()

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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Info("shutting down")
	grpcServer.GracefulStop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	apiServer.Shutdown(shutdownCtx)
	log.Info("shutdown complete")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustEnv(log *zap.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatal("required environment variable not set", zap.String("var", key))
	}
	return v
}
