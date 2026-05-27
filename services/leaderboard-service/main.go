// Package main is the entry point for the Leaderboard Service.
//
// The service:
//  1. Connects to Redis for live leaderboard state and pub/sub.
//  2. Connects to TimescaleDB (PostgreSQL) as a fallback when Redis is unavailable.
//  3. Exposes a REST endpoint GET /v1/leaderboard for initial page loads.
//  4. Serves WebSocket connections for live score-update fan-out.
//  5. (Wired but not proto-generated) exposes a gRPC leaderboard interface.
//
// Environment variables (all optional; defaults shown):
//
//	HTTP_ADDR              listen address for REST + WebSocket (:8082)
//	DATABASE_URL           TimescaleDB DSN (postgresql://dbhp:dbhp@timescaledb:5432/dbhp)
//	REDIS_ADDR             Redis host:port (redis:6379)
//	LOG_LEVEL              debug|info|warn|error (info)
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/iicpc/dbhp/leaderboard-service/internal/handler"
	"github.com/iicpc/dbhp/leaderboard-service/internal/hub"
)

func main() {
	// ------------------------------------------------------------------ logger
	logLevel := envOrDefault("LOG_LEVEL", "info")
	log, err := newLogger(logLevel)
	if err != nil {
		// Can't use zap yet — fall back to stderr.
		os.Stderr.WriteString("failed to initialise logger: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	// ----------------------------------------------------------------- config
	httpAddr := envOrDefault("HTTP_ADDR", ":8082")
	redisAddr := envOrDefault("REDIS_ADDR", "redis:6379")
	databaseURL := envOrDefault("DATABASE_URL", "postgresql://dbhp:dbhp@timescaledb:5432/dbhp")
	_ = databaseURL // reserved for TimescaleDB fallback path

	// ------------------------------------------------------------------ redis
	rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	defer rdb.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		// Redis is not mandatory at start-up — the service can warm up and wait.
		log.Warn("redis not reachable at start-up; will retry on demand",
			zap.String("redis_addr", redisAddr),
			zap.Error(err),
		)
	} else {
		log.Info("redis connected", zap.String("redis_addr", redisAddr))
	}

	// -------------------------------------------------------------------- hub
	wsHub := hub.NewHub()
	go wsHub.Run(ctx)
	log.Info("websocket hub started")

	// ------------------------------------------------------------------ mux
	mux := http.NewServeMux()

	// Health probe.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// REST leaderboard snapshot — initial page load (Requirement 10.8).
	leaderboardHandler := handler.NewLeaderboardHandler(rdb)
	mux.Handle("GET /v1/leaderboard", leaderboardHandler)

	// WebSocket endpoint — live score push (Requirements 10.2, 10.6).
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	mux.HandleFunc("GET /v1/ws/leaderboard", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Warn("websocket upgrade failed", zap.Error(err))
			return
		}
		client := hub.NewClient(wsHub, conn)
		wsHub.Register(client)
		log.Info("websocket client connected",
			zap.String("remote_addr", r.RemoteAddr),
		)

		// Pump outbound messages until client disconnects or ctx is done.
		go func() {
			defer func() {
				wsHub.Unregister(client)
				conn.Close()
				log.Info("websocket client disconnected",
					zap.String("remote_addr", r.RemoteAddr),
				)
			}()
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-client.Send():
					if !ok {
						return
					}
					if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
						log.Debug("websocket write error",
							zap.String("remote_addr", r.RemoteAddr),
							zap.Error(err),
						)
						return
					}
				}
			}
		}()
	})

	// ----------------------------------------------------------------- server
	srv := &http.Server{
		Addr:         httpAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Info("leaderboard-service starting",
			zap.String("http_addr", httpAddr),
			zap.String("redis_addr", redisAddr),
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	log.Info("shutdown signal received")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("graceful shutdown error", zap.Error(err))
	}

	log.Info("leaderboard-service stopped",
		zap.Int64("ws_dropped_messages", wsHub.DroppedCount()),
	)
}

// newLogger creates a production zap logger writing JSON to stdout.
// level controls the minimum severity; defaults to "info" if empty.
func newLogger(level string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	if level != "" {
		if err := cfg.Level.UnmarshalText([]byte(level)); err != nil {
			return nil, err
		}
	}
	cfg.OutputPaths = []string{"stdout"}
	cfg.ErrorOutputPaths = []string{"stderr"}
	return cfg.Build()
}

// envOrDefault returns the environment variable value or def if unset/empty.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
