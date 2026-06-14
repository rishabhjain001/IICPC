# iicpc benchmarking platform

**Live leaderboard → https://rishabhjain001.github.io/IICPC/**

---

## what this is

A platform that lets hackathon contestants submit trading exchange implementations (matching engines, order routers) and automatically benchmark them under synthetic HFT load. Submissions get scored on latency, throughput, and order-matching correctness, then ranked on a live leaderboard that updates in real time.

## why I built this

I wanted the IICPC hackathon to have actual objective scoring instead of judges eyeballing code. The idea was: if you wrote a fast, correct exchange, the numbers should prove it — not a presentation. Building the infrastructure to do that fairly (isolated sandboxes, deterministic correctness checking, sub-ms telemetry) turned out to be the interesting problem.

## architecture

```
contestant
    |
    | HTTPS upload
    v
submission-engine ──► artifact registry
    |
    | triggers
    v
build-pipeline (hermetic k8s job)
    |
    | OCI image push
    v
sandbox-controller (k8s operator)
    |
    | deploys + exposes endpoints
    v
bot-fleet-manager ──► synthetic bots ──► sandbox (FIX / REST / WS)
                                              |
                                              | telemetry events
                                              v
                                       kafka/redpanda
                                              |
                                              v
                                      telemetry-ingester
                                         |         |
                              timescaledb           redis pub/sub
                                                       |
                                               leaderboard-service
                                                       |
                                               websocket push
                                                       |
                                             browser (pages.github.io)
```

## scoring

```
speed     = clamp((100 - p99_ms) / 99,  0, 1)   # 1ms → 1.0, 100ms → 0.0
stability = clamp(max_tps / 1_000_000,  0, 1)   # 1M TPS → 1.0
accuracy  = correct_fills / total_fills          # from reference matching engine

composite = 0.35*speed + 0.35*stability + 0.30*accuracy
```

Contestants keep their highest composite score across all runs.

## stack

- Go — submission engine, build pipeline, sandbox controller, bot fleet, telemetry ingester, leaderboard service, control plane
- Rust — reference matching engine (pure function, no side effects, compiled to .so)
- React + TypeScript — leaderboard frontend
- TimescaleDB — raw telemetry samples and aggregates (hypertables by run_id + time)
- Redis — live leaderboard sorted set, pub/sub fan-out, rate limiting
- Kafka/Redpanda — telemetry event bus between bots and ingester
- Kubernetes — sandbox isolation (seccomp, AppArmor, cgroups), bot scheduling

## running locally

```bash
cp .env.example .env
docker compose up -d
```

Takes ~5 min on first run (building images). Once up, the leaderboard is at http://localhost:3000.

To populate sample data without running a full benchmark:

```bash
cd tools/seed
GOWORK=off go run .
```

## what I learned / known limitations

- The Kubernetes-dependent services (bot fleet, sandbox controller, build pipeline) idle gracefully in docker compose but actually need a real cluster to do anything useful. Getting them to degrade cleanly without crashing took more work than expected.
- The reference matching engine is deterministic and fast but I haven't tested it against edge cases like crossed orders arriving out of sequence from multiple bots simultaneously — that's the main correctness gap.
- TimescaleDB's hypertable compression policy works great for retention but the rolling p99 computation over a 60s window with a min-heap per run will get slow above ~50 concurrent runs. Would switch to a streaming percentile approximation (t-digest) for scale.
- WebSocket fan-out to 500+ clients works fine but the current hub drops messages to slow clients silently — for a real competition you'd want visible backpressure metrics.

## layout

```
services/        go microservices + react frontend
libs/rme/        rust matching engine
libs/shared-go/  shared types and redis key constants
migrations/      postgres + timescaledb sql
proto/           protobuf definitions
schemas/         avro schemas for kafka topics
tools/seed/      script to load sample leaderboard data
docs/            github pages (live leaderboard)
```
