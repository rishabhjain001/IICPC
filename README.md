# IICPC — Distributed Benchmarking Platform

Platform for the IICPC Summer Hackathon 2026. Contestants upload trading infrastructure (matching engines, order routers, market data processors) written in C++, Rust, or Go. The platform builds each submission, runs it under simulated HFT load, validates correctness, and ranks results on a live leaderboard.

## Stack

- **Go** — 7 microservices (submission, build, sandbox, bots, telemetry, leaderboard, control plane)
- **Rust** — deterministic reference matching engine
- **React + TypeScript** — live leaderboard frontend
- **TimescaleDB** — telemetry hypertables
- **Redis** — leaderboard state, pub/sub, rate limiting
- **Kafka/Redpanda** — telemetry event bus
- **Kubernetes** — sandbox isolation, bot fleet scheduling

## Services

| Service | Port |
|---|---|
| Submission Engine | :8443 |
| Leaderboard Frontend | :3000 |
| Control Plane API (gRPC) | :9090 |
| Prometheus metrics | :9091 |

## Running locally

```bash
cp .env.example .env
docker compose up -d
```

Open **http://localhost:3000**

First run builds all images (~5 min). Subsequent starts are fast.

## Structure

```
services/          # Go microservices + React frontend
libs/rme/          # Rust matching engine
libs/shared-go/    # Shared Go types and Redis keys
migrations/        # PostgreSQL + TimescaleDB SQL
proto/             # Protobuf definitions
schemas/           # Avro schemas for Kafka
docker-compose.yml
```
