// seed pushes sample leaderboard data into the running Redis instance
// so the frontend shows realistic standings immediately.
//
// Usage:
//   go run ./tools/seed
//   go run ./tools/seed -redis=localhost:6379
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// sampleContestant holds the sample data for one contestant.
type sampleContestant struct {
	Handle          string
	P99Ms           float64
	MaxTPS          int64
	FillAccuracy    float64
	RunStatus       string
	BenchmarkRunID  string
}

func main() {
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	flag.Parse()

	rdb := goredis.NewClient(&goredis.Options{Addr: *redisAddr})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("cannot reach Redis at %s: %v", *redisAddr, err)
	}
	fmt.Printf("Connected to Redis at %s\n", *redisAddr)

	contestants := []sampleContestant{
		{"team-alpha",    1.2,  920000, 0.981, "COMPLETE", newUUID()},
		{"speedrunner99", 2.1,  870000, 0.944, "COMPLETE", newUUID()},
		{"rustacean",     1.8,  800000, 0.967, "COMPLETE", newUUID()},
		{"gopher_hft",    3.4,  750000, 0.921, "COMPLETE", newUUID()},
		{"matchme",       4.7,  690000, 0.898, "COMPLETE", newUUID()},
		{"orderflow",     6.2,  610000, 0.875, "COMPLETE", newUUID()},
		{"latency_lab",   8.9,  540000, 0.852, "COMPLETE", newUUID()},
		{"ByteTrader",   12.3,  480000, 0.831, "COMPLETE", newUUID()},
		{"nullptr_exc",  18.7,  390000, 0.794, "COMPLETE", newUUID()},
		{"segfault_co",  25.0,  310000, 0.741, "COMPLETE", newUUID()},
		{"team-beta",     5.5,  620000, 0.901, "IN_PROGRESS", newUUID()},
		{"algo_wizard",   9.1,  510000, 0.862, "IN_PROGRESS", newUUID()},
	}

	const (
		wSpeed     = 0.35
		wStability = 0.35
		wAccuracy  = 0.30
	)
	clamp := func(v, lo, hi float64) float64 {
		if v < lo { return lo }
		if v > hi { return hi }
		return v
	}

	// Clear existing leaderboard
	rdb.Del(ctx, "leaderboard:current")

	now := time.Now().UTC()

	for _, c := range contestants {
		speed     := clamp((100-c.P99Ms)/99, 0, 1)
		stability := clamp(float64(c.MaxTPS)/1_000_000, 0, 1)
		composite := wSpeed*speed + wStability*stability + wAccuracy*c.FillAccuracy

		// Push score to sorted set
		if err := rdb.ZAdd(ctx, "leaderboard:current", goredis.Z{
			Score:  composite,
			Member: c.Handle,
		}).Err(); err != nil {
			log.Printf("ZADD %s: %v", c.Handle, err)
			continue
		}

		// Store full detail in a hash so the REST handler can return it
		detail := map[string]any{
			"composite_score":   fmt.Sprintf("%.6f", composite),
			"speed_score":       fmt.Sprintf("%.6f", speed),
			"stability_score":   fmt.Sprintf("%.6f", stability),
			"accuracy_score":    fmt.Sprintf("%.6f", c.FillAccuracy),
			"p99_latency_ms":    fmt.Sprintf("%.3f", c.P99Ms),
			"max_tps":           fmt.Sprintf("%d", c.MaxTPS),
			"fill_accuracy_pct": fmt.Sprintf("%.2f", c.FillAccuracy*100),
			"run_status":        c.RunStatus,
			"benchmark_run_id":  c.BenchmarkRunID,
		}
		if err := rdb.HSet(ctx, "leaderboard:meta:"+c.Handle, detail).Err(); err != nil {
			log.Printf("HSET %s: %v", c.Handle, err)
		}

		// Publish a SCORE_UPDATE so any open WebSocket connections update live
		update := map[string]any{
			"type":              "SCORE_UPDATE",
			"benchmark_run_id":  c.BenchmarkRunID,
			"contestant_handle": c.Handle,
			"composite_score":   composite,
			"speed_score":       speed,
			"stability_score":   stability,
			"accuracy_score":    c.FillAccuracy,
			"p99_latency_ms":    c.P99Ms,
			"max_tps":           c.MaxTPS,
			"fill_accuracy_pct": c.FillAccuracy * 100,
			"run_status":        c.RunStatus,
			"timestamp":         now.Format(time.RFC3339Nano),
		}
		payload, _ := json.Marshal(update)
		if err := rdb.Publish(ctx, "pubsub:leaderboard", payload).Err(); err != nil {
			log.Printf("PUBLISH %s: %v", c.Handle, err)
		}

		fmt.Printf("  seeded %-20s composite=%.4f  (%s)\n", c.Handle, composite, c.RunStatus)
	}

	fmt.Println("\nDone. Open http://localhost:3000 — leaderboard should show", len(contestants), "contestants.")
	fmt.Println("\nEndpoints:")
	fmt.Println("  http://localhost:3000                  — Live leaderboard UI")
	fmt.Println("  http://localhost:8082/v1/leaderboard   — REST API (JSON)")
	fmt.Println("  ws://localhost:8082/v1/ws/leaderboard  — WebSocket push")
	fmt.Println("  https://localhost:8443/v1/submissions  — Submit a trading engine")
	fmt.Println("  http://localhost:9090                  — Control Plane gRPC")
	fmt.Println("  http://localhost:9091/metrics          — Prometheus metrics")
}

// newUUID returns a pseudo-random UUID-like string (no external deps needed).
func newUUID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x",
		r.Uint32(), r.Uint32()&0xffff, r.Uint32()&0xffff,
		r.Uint32()&0xffff, r.Uint64()&0xffffffffffff)
}
