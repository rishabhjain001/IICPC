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
	log, err := newLogger(envOrDefault("LOG_LEVEL", "info"))
	if err != nil {
		os.Stderr.WriteString("failed to init logger: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	httpAddr := envOrDefault("HTTP_ADDR", ":8082")
	redisAddr := envOrDefault("REDIS_ADDR", "redis:6379")

	rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	defer rdb.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		log.Warn("redis not reachable at start-up; will retry on demand",
			zap.String("addr", redisAddr), zap.Error(err))
	} else {
		log.Info("redis connected", zap.String("addr", redisAddr))
	}

	wsHub := hub.NewHub()
	go wsHub.Run(ctx)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.Handle("GET /v1/leaderboard", handler.NewLeaderboardHandler(rdb))

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	mux.HandleFunc("GET /v1/ws/leaderboard", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Warn("ws upgrade failed", zap.Error(err))
			return
		}
		client := hub.NewClient(wsHub, conn)
		wsHub.Register(client)
		log.Info("ws client connected", zap.String("remote", r.RemoteAddr))

		go func() {
			defer func() {
				wsHub.Unregister(client)
				conn.Close()
				log.Info("ws client disconnected", zap.String("remote", r.RemoteAddr))
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
						return
					}
				}
			}
		}()
	})

	srv := &http.Server{
		Addr:         httpAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Info("leaderboard-service starting", zap.String("addr", httpAddr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("shutdown error", zap.Error(err))
	}

	log.Info("stopped", zap.Int64("ws_dropped", wsHub.DroppedCount()))
}

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

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
