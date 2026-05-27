# Implementation Plan: Distributed Benchmarking and Hosting Platform (DBHP)

## Overview

This plan implements the DBHP in incremental slices scoped for a competition/demo environment: data models and shared types first, then each microservice in dependency order (Submission Engine → Build Pipeline → Sandbox Controller → Bot Fleet Manager → Telemetry Ingester + RME → Leaderboard Service → Control Plane API → Frontend), with a docker-compose deployment descriptor last. Only the four most critical property-based tests are retained; all other optional test tasks are standard unit tests. The result is a fully working end-to-end system without full production hardening.

## Tasks

- [x] 1. Shared types, database schema, and project scaffolding
  - Create the monorepo directory structure with workspace-level configs for Rust (Cargo workspace) and Go modules
  - Define shared Protobuf definitions for all gRPC services and Kafka Avro schemas
  - Write and apply PostgreSQL migration scripts for `contestants`, `contestant_tokens`, `submissions`, `submission_rate_limits`, `benchmark_runs`, and `benchmark_run_audit` tables
  - Write and apply TimescaleDB hypertable setup for `telemetry_samples`, `telemetry_aggregates`, and `fill_events`
  - Define Redis key schema constants used across services
  - _Requirements: 7.4, 8.1, 9.1, 11.3_


- [x] 2. Submission Engine — authentication, validation, and upload
  - [x] 2.1 Implement the HTTPS multipart upload endpoint (`POST /v1/submissions`) with bearer-token authentication middleware; wire contestant token lookup against `contestant_tokens` table (SHA-256 hash comparison); return HTTP 401 on invalid/missing tokens
    - _Requirements: 1.1_

  - [x] 2.2 Implement artifact format validation (ELF magic-byte detection and archive root-manifest inspection), size cap (500 MB → HTTP 413), and SHA-256 checksum verification against `X-Checksum-SHA256` (HTTP 415 / HTTP 422 on failure, delete partial data on mismatch)
    - _Requirements: 1.2, 1.3, 1.4, 1.5_

  - [x] 2.3 Implement rolling-window rate limiter (5 uploads / 60 min per contestant) backed by a Redis sorted set, returning HTTP 429 with `Retry-After` on excess
    - _Requirements: 1.6, 1.7_

  - [x] 2.4 Implement Artifact Registry push with UUID Submission ID assignment; return HTTP 201 with `submission_id`; return HTTP 503 if registry is unavailable and do not assign an ID
    - _Requirements: 1.8, 1.9, 1.10_

  - [x] 2.5 Implement `GET /v1/submissions/{submission_id}/status` endpoint returning current status and build log URL
    - _Requirements: 2.4, 2.6_

- [x] 3. Checkpoint — Ensure all Submission Engine tests pass
  - Ensure all tests pass, ask the user if questions arise.

- [x] 4. Build Pipeline (Hermetic Job Controller)
  - [x] 4.1 Implement the Kubernetes Job spawner that creates a hermetic build job within 60 seconds of artifact storage; enforce no-egress NetworkPolicy, non-root security context, and `automountServiceAccountToken: false`; set status to `BUILD_INFRASTRUCTURE_ERROR` if the job fails to start
    - _Requirements: 2.1, 2.2_

  - [x] 4.2 Implement build execution: detect manifest type (`Makefile` / `Cargo.toml` / `go.mod`), run the correct pinned toolchain (GCC 13 / Clang 17, Rust 1.77, Go 1.22); enforce 10-minute TTL; capture full build log (stdout + stderr, capped at 10 MB with middle truncation) and set `BUILD_TIMEOUT` or `BUILD_FAILED` on failure
    - _Requirements: 2.2, 2.3, 2.4_

  - [x] 4.3 Implement post-build OCI image assembly using `buildah`; enumerate dynamic deps via `ldd`/`go tool nm`; push minimal image tagged with Submission ID to the Artifact Registry; set `BUILD_PUBLISH_FAILED` on push failure
    - _Requirements: 2.5, 2.6_

  - [ ]* 4.4 Write unit tests for build log truncation and OCI push failure handling
    - Test that a failed build captures a non-zero-length log and sets status `BUILD_FAILED`
    - Test that a push failure after a successful build sets status `BUILD_PUBLISH_FAILED`
    - _Requirements: 2.4, 2.6_


- [x] 5. Sandbox Controller (Kubernetes Operator)
  - [x] 5.1 Implement the `Sandbox` CRD and operator reconcile loop; apply all security hardening in a single pod spec: seccomp-bpf allowlist profile, AppArmor profile, `readOnlyRootFilesystem`, non-root UID ≥ 1000, dropped ALL capabilities, `allowPrivilegeEscalation: false`, cgroup v2 `memory.max = 4 GiB` and `pids.max = 512`, static CPU Manager policy for dedicated core pinning via `cpuset.cpus`, and ephemeral tmpfs volume (1 GiB) for `/tmp`
    - _Requirements: 3.1, 3.2, 3.3, 3.5_

  - [x] 5.2 Implement per-run isolated overlay network using Multus CNI macvlan + NetworkPolicy restricting ingress to Bot Fleet Manager and Telemetry Ingester CIDRs only; abort deployment and set status to `NETWORK_SETUP_FAILED` if overlay creation fails
    - _Requirements: 3.4_

  - [x] 5.3 Implement crash detection via Kubernetes pod watch; on unexpected container exit, record termination reason and exit code, set run status to `SANDBOX_CRASH`, and release all reserved resources within 30 s; implement 2-hour maximum lifetime enforcement with graceful stop and final metric collection (set `COLLECTION_TIMEOUT` if collection exceeds 60 s)
    - _Requirements: 3.6, 3.7_

  - [x] 5.4 Implement health-check polling at 5-second intervals; after 3 consecutive failures, set Sandbox status to `UNHEALTHY` and Benchmark Run status to `FAILED`
    - _Requirements: 3.8_

  - [x] 5.5 Implement endpoint registration: expose FIX on TCP 9898, REST on TCP 8080, and WebSocket on TCP 8081; register in service registry within 5 s of `RUNNING` state; TCP-probe each endpoint within 15 s, marking unresponsive ones `UNAVAILABLE`; if all endpoints are `UNAVAILABLE`, set run status to `NO_ENDPOINTS`
    - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.6_

  - [x] 5.6 Implement the `SandboxController` gRPC service (`GetSandboxEndpoints`, `GetSandboxStatus`, `TerminateSandbox`); return only `AVAILABLE` endpoints from `GetSandboxEndpoints`; return gRPC `NOT_READY` when sandbox has not reached `RUNNING`
    - _Requirements: 4.5, 4.6, 4.7_

- [x] 6. Checkpoint — Ensure all Sandbox Controller tests pass
  - Ensure all tests pass, ask the user if questions arise.


- [x] 7. Bot Fleet Manager
  - [x] 7.1 Implement the bot scheduling algorithm: query `role=bot-worker` node pool capacities, divide the fleet evenly across available nodes using static assignment (target bots per node = `ceil(fleet_size / node_count)`), enforcing per-node cap of 500; assign each bot a scenario using weighted random sampling from the five scenario types (`MARKET_MAKER`, `AGGRESSIVE_TAKER`, `CANCEL_SPAMMER`, `MIXED_RETAIL`, `LATENCY_PROBER`) according to the configured `ScenarioDistribution`
    - _Requirements: 5.1, 5.2, 5.4, 5.5_

  - [x] 7.2 Implement fleet provisioning timeout enforcement: all bots `READY` within 60 s (≤ 1,000 bots) or 180 s (≤ 10,000 bots); set run status to `PROVISIONING_TIMEOUT` and release partial fleet on deadline miss
    - _Requirements: 5.3_

  - [x] 7.3 Implement graceful fleet shutdown on `STOP` command or run end: terminate all bots within 30 s; forcibly kill any remaining bots at the 30 s timeout
    - _Requirements: 5.6_

  - [x] 7.4 Implement worker-node heartbeat monitor (5-second heartbeat, 15-second missed-heartbeat threshold); redistribute affected bots to healthy nodes within 45 s; set run status to `INSUFFICIENT_CAPACITY` if no capacity found
    - _Requirements: 5.7_

  - [x] 7.5 Implement the `BotFleetManager` gRPC service (`ProvisionFleet`, `StopFleet`, `GetFleetStatus`)
    - _Requirements: 5.1, 5.3, 5.6_

- [x] 8. Synthetic Trading Bot — message generation
  - [x] 8.1 Implement FIX 4.2 message generator producing Limit Order, Market Order, and Cancel/Replace messages with a globally unique `ClOrdID` per Benchmark Run; embed per-bot-session sequence number (starts at 1, strictly increasing) and nanosecond-resolution send timestamp in every message
    - _Requirements: 6.1, 6.4, 6.7_

  - [x] 8.2 Implement REST message generator (HTTP POST/DELETE with JSON body schema); unique request ID per message; same sequence-number and timestamp embedding as FIX
    - _Requirements: 6.2, 6.4, 6.7_

  - [x] 8.3 Implement `LATENCY_PROBER` rate cap (≤ 100 msg/s per bot) with microsecond-precision send timestamp forwarded to Telemetry Ingester; implement `AGGRESSIVE_TAKER` rate targeting (≥ 10,000 msg/s) with back-pressure detection (HTTP 429 for REST, TCP send buffer stall > 500 ms for FIX) and rate reduction within 1 s
    - _Requirements: 6.5, 6.6_

- [x] 9. Checkpoint — Ensure all Bot Fleet Manager and Bot message-generation tests pass
  - Ensure all tests pass, ask the user if questions arise.


- [x] 10. Reference Matching Engine (Rust)
  - [x] 10.1 Implement the `OrderBook` data structure in Rust (`BTreeMap<Price, VecDeque<Order>>` for bids descending and asks ascending, plus `HashMap<OrderId, (Side, Price)>` cancel index)
    - _Requirements: 8.6_

  - [x] 10.2 Implement the `match_order` pure function: crossing loop with price-time priority, fill event emission, partial-fill resting on book, cancel handling; no I/O, no global state, no timestamps in matching logic
    - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.6_

  - [x] 10.3 Implement `run_matching_engine(orders: &[OrderEvent]) -> MatchResult` as the public library entry point; compile to `libdbhp_rme.so` and a WASM module
    - _Requirements: 8.6_

  - [x]* 10.4 Write property test for Reference Matching Engine determinism
    - **Property 15: Reference Matching Engine Determinism**
    - For any ordered sequence of order events `S`, `RME(S)` run twice SHALL produce identical fill sequences (same fills, same order, same prices, quantities, and order IDs)
    - **Validates: Requirements 8.6, 8.7**

  - [x]* 10.5 Write property test for Reference Matching Engine idempotency under cancellation
    - **Property 16: Reference Matching Engine Idempotency Under Cancellation**
    - For any valid order sequence `S`, let `F = RME(S).fills`. Running `RME(S ++ [cancel(f) for f in F])` SHALL leave the order book with all quantities non-negative and no fully-filled order remaining on the book
    - **Validates: Requirements 8.8**

- [x] 11. Telemetry Ingester
  - [x] 11.1 Implement Kafka consumer group ingestion from `telemetry.raw.{env}` and `telemetry.fills.{env}` topics partitioned by `benchmark_run_id`; deserialize Avro events via schema registry
    - _Requirements: 7.1_

  - [x] 11.2 Implement direct TimescaleDB insert pipeline with rolling latency computation (min-heap per run, 60-second window, 5-second update interval) computing p50, p90, p99 with 1-microsecond resolution; full-population computation at run completion; implement rolling maximum TPS calculation over a 5-second sliding window
    - _Requirements: 7.2, 7.3, 7.7, 7.8_

  - [x] 11.3 Implement raw sample and aggregate persistence to TimescaleDB hypertables within 500 ms; implement error event recording for HTTP 4xx/5xx and FIX session rejects; publish aggregated metrics to Redis pub/sub channel `pubsub:metrics:{run_id}` at minimum once per second
    - _Requirements: 7.4, 7.5, 7.6_

  - [x] 11.4 Implement correctness validation by invoking the RME for each fill event and flagging `PRICE_PRIORITY_VIOLATION`, `TIME_PRIORITY_VIOLATION`, and `QUANTITY_MISMATCH`; compute Fill Accuracy Score as `correct_fills / total_fills` in [0.0, 1.0]
    - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5_

- [x] 12. Checkpoint — Ensure all Telemetry Ingester and RME tests pass
  - Ensure all tests pass, ask the user if questions arise.


- [x] 13. Leaderboard Service
  - [x] 13.1 Implement composite score computation: `SpeedScore = clamp((100 - p99_ms) / 99, 0.0, 1.0)`, `StabilityScore = clamp(max_tps / 1_000_000, 0.0, 1.0)`, `AccuracyScore = fill_accuracy` (0.0 if unavailable), `CompositeScore = 0.35*SpeedScore + 0.35*StabilityScore + 0.30*AccuracyScore`; recompute within 2 s of metric batch arrival; assign CompositeScore = 0.0 for runs ending in any terminal failure status
    - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.5, 9.6_

  - [x]* 13.2 Write property test for Composite Score weight invariant
    - **Property 19: Composite Score Weight Invariant**
    - For any triple `(SpeedScore, StabilityScore, AccuracyScore)` each in `[0.0, 1.0]`, `CompositeScore = 0.35*S + 0.35*St + 0.30*A` SHALL be in `[0.0, 1.0]`; weight sum SHALL equal 1.0 exactly; CompositeScore SHALL be 0.0 when all inputs are 0.0
    - **Validates: Requirements 9.1, 9.4**

  - [x] 13.3 Implement per-contestant historical maximum standing: persist all Benchmark Run scores to `benchmark_runs` table and maintain `leaderboard:current` Redis sorted set using the maximum composite score across all runs; implement Redis pub/sub score publishing with 3× exponential backoff (100 ms / 200 ms / 400 ms) on failure
    - _Requirements: 9.7, 9.8_

  - [x]* 13.4 Write property test for historical maximum leaderboard standing
    - **Property 20: Historical Maximum Leaderboard Standing**
    - For any contestant with composite scores `[c₁, …, cₙ]`, their leaderboard standing SHALL equal `max(c₁, …, cₙ)`; adding a run with score `cₙ₊₁` SHALL update the standing to `max(standing, cₙ₊₁)`
    - **Validates: Requirements 9.7**

  - [x] 13.5 Implement the WebSocket fan-out hub: single JSON serialization per update broadcast to all connected clients (≥ 500); drop messages to slow clients and record a metric; wire to `pubsub:leaderboard` Redis channel
    - _Requirements: 10.1, 10.2, 10.6_

  - [x] 13.6 Implement `GET /v1/leaderboard` REST initial-snapshot endpoint: fetch from `leaderboard:current` Redis sorted set; fall back to TimescaleDB if Redis unavailable; return paginated JSON with `ETag`
    - _Requirements: 10.8, 11.5, 11.6_

  - [x] 13.7 Implement the `LeaderboardService` gRPC endpoints (`GetLeaderboard`, `GetContestantScore`, `StreamLeaderboard`) with paginated results and p99 response time ≤ 50 ms for pages up to 100 contestants
    - _Requirements: 11.1, 11.6_

- [x] 14. Checkpoint — Ensure all Leaderboard Service tests pass
  - Ensure all tests pass, ask the user if questions arise.


- [x] 15. Control Plane API
  - [x] 15.1 Implement `ContestantService` gRPC handlers (`RegisterContestant`, `IssueToken`, `RevokeToken`) with token storage in `contestant_tokens` (SHA-256 hashed); implement `SubmissionService` gRPC handlers (`GetSubmissionStatus`, `GetBuildLog` streaming) backed by `submissions` table and Artifact Registry
    - _Requirements: 11.1_

  - [x] 15.2 Implement `BenchmarkService` gRPC handlers (`CreateRun`, `GetRun`, `ListRuns`, `CancelRun`) with full state machine enforcement (`QUEUED → BUILDING → DEPLOYING → RUNNING → COLLECTING → SCORING → COMPLETE` and all terminal failure states); write each transition to `benchmark_run_audit` table
    - _Requirements: 11.1, 11.3, 11.7_

  - [x] 15.3 Implement concurrent run limit enforcement: reject a `CreateRun` request with gRPC `RESOURCE_EXHAUSTED` if the contestant already has 3 non-terminal runs
    - _Requirements: 11.4_

  - [x] 15.4 Implement bearer token middleware for all internal service-to-service gRPC calls; validate a shared secret token on each inbound call and return gRPC `UNAUTHENTICATED` on missing or invalid tokens; external contestant authentication (HTTP 401 on invalid bearer token, Requirement 1.1) is handled separately by the Submission Engine and is unchanged; implement Prometheus-compatible `/metrics` endpoint exposing active Sandbox count, active bot count, Kafka consumer lag per topic, TimescaleDB write latency p99, and Control Plane request rate
    - _Requirements: 11.2, 11.5_

- [x] 16. Leaderboard Frontend (browser SPA)
  - [x] 16.1 Implement the ranked standings table displaying rank, contestant handle, CompositeScore, SpeedScore, StabilityScore, AccuracyScore, p99 latency (ms), max TPS, Fill Accuracy %, and Benchmark Run status; implement initial-load HTTP fetch of leaderboard snapshot before WebSocket connect
    - _Requirements: 10.3, 10.8_

  - [x] 16.2 Implement the WebSocket client: establish connection to Leaderboard Service, receive and apply `SCORE_UPDATE` push events, update standings within 2 s; implement reconnect with exponential backoff (1 s base, 30 s cap); show last-known data during disconnection and request full snapshot on reconnect
    - _Requirements: 10.1, 10.2, 10.5_

  - [x] 16.3 Implement per-contestant live time-series chart for p50/p90/p99 latency and TPS (updated ≥ once per second); show "No data available" when no Benchmark Run exists; implement live progress indicator for `IN_PROGRESS` runs (elapsed time, current TPS, latest p99 latency)
    - _Requirements: 10.4, 10.7_

  - [x]* 16.4 Write unit tests for WebSocket reconnect backoff logic and initial-snapshot fallback
    - _Requirements: 10.5, 10.8_

- [x] 17. Checkpoint — Ensure all Control Plane API and Frontend tests pass
  - Ensure all tests pass, ask the user if questions arise.


- [x] 18. Structured JSON logging and docker-compose deployment descriptor
  - [x] 18.1 Implement structured JSON logging (DEBUG / INFO / WARN / ERROR levels, including `benchmark_run_id` and `submission_id` correlation fields) in all microservices; write to stdout so that any log aggregator or `docker compose logs` can consume them without additional configuration
    - _Requirements: 13.2_

  - [x] 18.2 Write a `docker-compose.yml` covering all services for local and demo deployment: Submission Engine, Build Pipeline, Sandbox Controller, Bot Fleet Manager, Telemetry Ingester, Reference Matching Engine (sidecar or shared lib mount), Leaderboard Service + Frontend, Control Plane API, Kafka/Redpanda, TimescaleDB, Redis, and Artifact Registry; define named volumes, a shared internal network, health-check directives, and environment variable placeholders for all secrets and external endpoints
    - _Requirements: 12.2, 12.4_

- [x] 19. Final checkpoint — Ensure all tests pass and docker-compose starts cleanly
  - Run `docker compose up --wait` and verify all services reach healthy status
  - Ensure all unit, property, and integration tests pass
  - Ask the user if any questions arise.


## Notes

- Tasks marked with `*` are optional and can be skipped for a faster MVP iteration
- Only four property-based tests are included (Properties 15, 16, 19, 20) — the highest-value invariants for correctness and scoring
- PBT library: **proptest** (Rust) for the RME and scoring formulas; each runs a minimum of 100 iterations with seed-controlled randomness
- Checkpoints at tasks 3, 6, 9, 12, 14, 17, and 19 ensure incremental validation
- Deployment uses a single `docker-compose.yml` for local/demo environments; no Helm charts, no Terraform, no NetworkPolicies as separate artifacts
- Observability is limited to structured JSON logging and the existing `/metrics` endpoint; no OpenTelemetry tracing, no Prometheus alerting rules, no PodDisruptionBudgets
- Bot Fleet Manager uses static even-division scheduling (no round-robin spread-constraint enforcement or ±5% deviation guard); the per-node cap of 500 is the only hard constraint
- Telemetry Ingester uses direct TimescaleDB inserts with no in-memory ring buffer back-pressure and no Redis-based deduplication; Kafka consumer group offsets provide at-least-once delivery
- Bots communicate with the Sandbox over two protocol modes only: FIX and REST. The Sandbox still exposes WebSocket on port 8081 (it is part of the tested surface), but bots do not use it
- Internal service-to-service authentication uses a shared bearer token (not mTLS); external contestant auth (HTTP 401) is unchanged
- The RME Rust crate is a pure function library shared by the Telemetry Ingester and the WASM test harness

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1"] },
    { "id": 1, "tasks": ["2.1", "4.1", "5.1", "10.1"] },
    { "id": 2, "tasks": ["2.2", "2.3", "4.2", "5.2", "5.3", "10.2", "15.1"] },
    { "id": 3, "tasks": ["2.4", "2.5", "4.3", "5.4", "5.5", "10.3", "15.2"] },
    { "id": 4, "tasks": ["4.4", "5.6", "7.1", "8.1", "10.4", "10.5", "15.3", "15.4"] },
    { "id": 5, "tasks": ["7.2", "7.3", "7.4", "7.5", "8.2", "8.3", "11.1", "16.1"] },
    { "id": 6, "tasks": ["11.2", "11.3", "13.1", "16.2", "16.3"] },
    { "id": 7, "tasks": ["11.4", "13.2", "13.3", "13.5", "13.6", "13.7", "16.4"] },
    { "id": 8, "tasks": ["13.4", "18.1"] },
    { "id": 9, "tasks": ["18.2"] }
  ]
}
```
