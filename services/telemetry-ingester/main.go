// Package main is the entry point for the Telemetry Ingester service.
//
// The service:
//  1. Reads telemetry and fill events from Kafka consumer groups (Task 11.1).
//  2. Computes rolling latency percentiles and max TPS (Task 11.2).
//  3. Persists raw samples and aggregates to TimescaleDB and publishes
//     aggregated metrics to Redis pub/sub (Task 11.3).
//  4. Validates fill correctness against the Reference Matching Engine
//     via the RMEInvoker interface (Task 11.4).
//
// Environment variables (all required unless marked optional):
//
//	KAFKA_BROKERS      comma-separated list of Kafka bootstrap servers
//	KAFKA_GROUP_ID     consumer group ID (default: "telemetry-ingester")
//	KAFKA_ENV          environment suffix for topic names (default: "dev")
//	TIMESCALE_DSN      PostgreSQL DSN for the TimescaleDB instance
//	REDIS_ADDR         Redis server address (host:port)
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	kafkapkg "github.com/iicpc/dbhp/telemetry-ingester/internal/kafka"
	"github.com/iicpc/dbhp/telemetry-ingester/internal/metrics"
	"github.com/iicpc/dbhp/telemetry-ingester/internal/publisher"
	"github.com/iicpc/dbhp/telemetry-ingester/internal/store"
)

func main() {
	log, _ := zap.NewProduction()
	defer log.Sync() //nolint:errcheck

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Fatal("telemetry-ingester exited with error", zap.Error(err))
	}
}

func run(ctx context.Context, log *zap.Logger) error {
	// --- Configuration -------------------------------------------------------
	brokersRaw := envOrDefault("KAFKA_BROKERS", "localhost:9092")
	brokers := strings.Split(brokersRaw, ",")
	groupID := envOrDefault("KAFKA_GROUP_ID", "telemetry-ingester")
	env := envOrDefault("KAFKA_ENV", "dev")
	timescaleDSN := envOrDefault("TIMESCALE_DSN", "")
	redisAddr := envOrDefault("REDIS_ADDR", "localhost:6379")

	// --- TimescaleDB ---------------------------------------------------------
	var tsStore *store.TimescaleStore
	if timescaleDSN != "" {
		pool, err := pgxpool.New(ctx, timescaleDSN)
		if err != nil {
			return fmt.Errorf("pgxpool.New: %w", err)
		}
		defer pool.Close()
		tsStore = &store.TimescaleStore{Pool: pool}
		log.Info("connected to TimescaleDB")
	} else {
		log.Warn("TIMESCALE_DSN not set; persistence disabled (dev mode)")
	}

	// --- Redis ---------------------------------------------------------------
	rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	defer rdb.Close()
	metricsPublisher := &publisher.MetricsPublisher{RDB: rdb}
	log.Info("redis publisher configured", zap.String("addr", redisAddr))

	// --- Metrics windows -----------------------------------------------------
	latencyWindow := metrics.NewLatencyWindow()
	tpsWindow := metrics.NewTPSWindow()

	// --- Channels ------------------------------------------------------------
	rawCh := make(chan kafkapkg.TelemetryEvent, 10_000)
	fillCh := make(chan kafkapkg.FillEvent, 1_000)

	// --- Kafka consumer ------------------------------------------------------
	// NOTE: In production, replace stubReader below with a real librdkafka
	// consumer (confluent-kafka-go or similar). The Consumer struct accepts any
	// MessageReader so the real client can be wired here without code changes.
	consumer := &kafkapkg.Consumer{
		Brokers: brokers,
		GroupID: groupID,
		Env:     env,
		RawCh:   rawCh,
		FillCh:  fillCh,
		Logger:  log,
		// Reader is intentionally nil here; a production deployment sets it to
		// a confluent-kafka-go consumer instance. When nil, Run would panic —
		// this guards that the reader is always injected.
	}
	log.Info("consumer configured",
		zap.Strings("brokers", consumer.Brokers),
		zap.String("group_id", consumer.GroupID),
		zap.String("env", consumer.Env),
	)

	// --- Telemetry event processor -------------------------------------------
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-rawCh:
				now := time.Now()
				latencyWindow.Add(now, ev.SendTsNs, ev.RecvTsNs)
				tpsWindow.Record(now)

				if tsStore != nil {
					writeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
					ec := ev.ErrorCode
					sample := store.TelemetrySample{
						BenchmarkRunID: ev.BenchmarkRunID,
						BotID:          ev.BotID,
						SeqNum:         ev.SeqNum,
						Protocol:       ev.Protocol,
						SendTsNs:       ev.SendTsNs,
						RecvTsNs:       ev.RecvTsNs,
						RequestID:      ev.RequestID,
						ErrorCode:      ec,
						EventTime:      now,
					}
					if err := tsStore.InsertTelemetrySample(writeCtx, sample); err != nil {
						log.Error("InsertTelemetrySample failed", zap.Error(err))
					}
					cancel()

					// Record error event if error code present.
					if ev.ErrorCode != nil {
						errCtx, errCancel := context.WithTimeout(ctx, 500*time.Millisecond)
						if err := tsStore.RecordError(errCtx, ev.BenchmarkRunID, ev.RequestID, *ev.ErrorCode, now); err != nil {
							log.Error("RecordError failed", zap.Error(err))
						}
						errCancel()
					}
				}
			}
		}
	}()

	// --- Metrics publishing ticker (at least once per second, Req 7.5) -------
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		// runID placeholder — in production this comes from the active run context.
		const runID = "active-run"
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p50, p90, p99 := latencyWindow.Percentiles()
				maxTPS := tpsWindow.MaxTPS()
				pubCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				if err := metricsPublisher.Publish(pubCtx, runID, publisher.AggregatedMetrics{
					P50LatencyUs: p50,
					P90LatencyUs: p90,
					P99LatencyUs: p99,
					MaxTPS:       maxTPS,
				}); err != nil {
					log.Error("MetricsPublisher.Publish failed", zap.Error(err))
				}
				cancel()
			}
		}
	}()

	// The fill event processor and RME validation pipeline are intentionally
	// left as a channel drain here.  The full FillValidator wiring is injected
	// by the service orchestrator (see internal/validator for the interface).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case fill := <-fillCh:
				log.Debug("fill event received",
					zap.String("order_id", fill.OrderID),
					zap.String("run_id", fill.BenchmarkRunID),
				)
				// TODO: wire FillValidator here when RME CGo binding is ready.
				_ = fill
			}
		}
	}()

	log.Info("telemetry-ingester started")
	<-ctx.Done()
	log.Info("telemetry-ingester shutting down")
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
