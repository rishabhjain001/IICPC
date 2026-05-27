# Requirements Document

## Introduction

The Distributed Benchmarking and Hosting Platform (DBHP) is the core infrastructure for the IICPC Summer Hackathon 2026. It enables contestants to upload trading infrastructure implementations (exchange matching engines, order routers, or market data processors) written in C++, Rust, or Go, and automatically benchmarks them under simulated high-frequency trading workloads. The platform containerizes each submission in a strictly isolated sandbox, spawns a distributed fleet of synthetic trading bots that hammer the submission with FIX, REST, and WebSocket traffic, captures granular latency and correctness telemetry, and surfaces a live composite leaderboard ranking all contestants in real time.

The system is a hardcore distributed systems project: it must sustain thousands of concurrent virtual market participants, process millions of order events per benchmark run, maintain sub-millisecond telemetry precision, and remain resilient to adversarial or malformed contestant code.

---

## Glossary

- **Platform**: The Distributed Benchmarking and Hosting Platform (DBHP) described in this document.
- **Submission**: A contestant-uploaded artifact containing a trading infrastructure implementation (binary or source code in C++, Rust, or Go).
- **Submission Engine**: The Platform subsystem responsible for receiving, validating, building, and registering Submissions.
- **Sandbox**: An isolated, resource-constrained container environment in which a single Submission is deployed and executed.
- **Sandbox Controller**: The Platform subsystem that creates, manages, configures, and destroys Sandboxes.
- **Bot Fleet Manager**: The Platform subsystem that provisions, orchestrates, and tears down Synthetic Trading Bots.
- **Synthetic Trading Bot**: An ephemeral worker process that generates and sends simulated trading messages (FIX, REST, or WebSocket) to a Sandbox endpoint.
- **Bot Scenario**: A parameterized workload profile defining the mix of order types, message rates, and protocol choices used by a cohort of Synthetic Trading Bots.
- **Benchmark Run**: A full end-to-end evaluation cycle: Sandbox deployment → Bot Fleet activation → workload execution → telemetry collection → score computation.
- **Telemetry Ingester**: The Platform subsystem that receives, timestamps, aggregates, and persists latency and correctness metrics from Benchmark Runs.
- **Leaderboard Service**: The Platform subsystem that computes composite scores from raw metrics and serves ranked results to the frontend.
- **Leaderboard Frontend**: The browser-based UI that displays live ranked standings and per-submission analytics.
- **Composite Score**: A weighted numeric ranking derived from speed (latency percentiles), stability (max TPS before degradation), and algorithmic accuracy (fill correctness, price-time priority).
- **FIX**: Financial Information eXchange protocol, version 4.2 or later.
- **REST Endpoint**: An HTTP/1.1 or HTTP/2 API endpoint exposed by a Submission for order management.
- **WebSocket Endpoint**: A persistent bidirectional connection endpoint exposed by a Submission for streaming order and market data.
- **TPS**: Transactions Per Second — the number of order-related messages processed by a Submission per second.
- **p50/p90/p99 Latency**: The 50th, 90th, and 99th percentile round-trip latency measured from the moment a Synthetic Trading Bot sends a message to the moment a response is received.
- **Price-Time Priority**: The matching rule under which orders at the same price level are filled in the order they were received.
- **Fill Accuracy**: The correctness of quantity and price assigned to order fills relative to the expected matching outcome.
- **Artifact Registry**: A secure internal container image and binary store where validated Submission images are stored prior to Sandbox deployment.
- **Control Plane**: The set of Platform services (API server, scheduler, orchestrator) that coordinate all subsystems.
- **TimescaleDB**: A time-series-optimized PostgreSQL extension used to store Telemetry data.
- **Redis**: An in-memory data store used for real-time score caching and leaderboard state.
- **Kafka / Redpanda**: A distributed event streaming platform used as the Telemetry message bus between subsystems.
- **IaC**: Infrastructure as Code — declarative configuration (Terraform, Kubernetes manifests) that provisions all Platform resources.

---

## Requirements

### Requirement 1: Secure Submission Upload

**User Story:** As a hackathon contestant, I want to upload my trading infrastructure code or binary securely, so that my submission is received intact and protected from tampering by other contestants.

#### Acceptance Criteria

1. THE Submission Engine SHALL accept uploads via an authenticated HTTPS multipart endpoint requiring a valid contestant API token; IF the token is missing or invalid, THE Submission Engine SHALL return an HTTP 401 response with a machine-readable error body.
2. THE Submission Engine SHALL accept submission artifacts in the following formats: pre-compiled ELF binaries (C++, Rust, Go) and source archives (`.tar.gz`, `.zip`) containing a root-level `Makefile` or `Cargo.toml` or `go.mod`; IF the submitted artifact is not one of these formats, THE Submission Engine SHALL reject the upload and return an HTTP 415 response with a machine-readable error body.
3. WHEN a submission artifact is received, THE Submission Engine SHALL verify the SHA-256 checksum of the artifact against a contestant-provided checksum before storing it.
4. IF a submitted artifact exceeds 500 MB, THEN THE Submission Engine SHALL reject the upload and return an HTTP 413 response with a machine-readable error body.
5. IF the SHA-256 checksum verification fails, THEN THE Submission Engine SHALL reject the artifact, delete any partial data, and return an HTTP 422 response with a machine-readable error body indicating checksum mismatch.
6. THE Submission Engine SHALL enforce a rate limit of at most 5 submission uploads per contestant per 60-minute rolling window.
7. IF a contestant exceeds the rate limit, THEN THE Submission Engine SHALL return an HTTP 429 response with a `Retry-After` header indicating the number of seconds until the next upload is permitted.
8. THE Submission Engine SHALL store accepted artifacts in the Artifact Registry within 30 seconds of a successful upload acknowledgment.
9. WHEN an artifact is stored in the Artifact Registry, THE Submission Engine SHALL assign a globally unique immutable Submission ID and return it in the HTTP 201 response body.
10. IF the Artifact Registry is unavailable when the Submission Engine attempts to store an accepted artifact, THEN THE Submission Engine SHALL return an HTTP 503 response with a machine-readable error body and SHALL NOT assign a Submission ID.

---

### Requirement 2: Source Build Pipeline

**User Story:** As a contestant submitting source code, I want the platform to build my code automatically and reproducibly, so that I do not need to manage cross-compilation or dependency resolution manually.

#### Acceptance Criteria

1. WHEN a source archive is accepted, THE Submission Engine SHALL trigger an isolated build job within 60 seconds of artifact storage; IF the build container fails to start within 60 seconds, THE Submission Engine SHALL mark the Submission status as `BUILD_INFRASTRUCTURE_ERROR` and deliver a status event to the contestant within 30 seconds.
2. THE Submission Engine SHALL execute builds inside a hermetic build container that has no outbound network access and contains only the officially pinned toolchain versions (GCC 13 / Clang 17 for C++, Rust 1.77 stable, Go 1.22).
3. IF a build job does not complete within 10 minutes, THEN THE Submission Engine SHALL terminate the build container, mark the Submission status as `BUILD_TIMEOUT`, and deliver a platform-side status event to the contestant within 30 seconds of the timeout.
4. IF the build exits with a non-zero status code, THEN THE Submission Engine SHALL capture the full build log (stdout and stderr, capped at 10 MB with excess lines truncated from the middle), store it in the Artifact Registry associated with the Submission ID, and mark the Submission status as `BUILD_FAILED`.
5. WHEN a build succeeds, THE Submission Engine SHALL produce an OCI-compliant container image containing only the compiled binary and the dynamic library dependencies reported by the toolchain's dependency inspection tool, and push it to the Artifact Registry tagged with the Submission ID.
6. IF the Artifact Registry push fails after a successful build, THE Submission Engine SHALL mark the Submission status as `BUILD_PUBLISH_FAILED` and deliver a status event to the contestant within 30 seconds.
7. WHEN a build succeeds, THE Submission Engine SHALL record the reproducible build manifest — including toolchain version, compiler flags passed to the build invocation, and cryptographic hashes of all declared dependencies — in the Artifact Registry alongside the image.

---

### Requirement 3: Sandboxed Deployment

**User Story:** As a platform operator, I want each submission to run in a strictly isolated sandbox, so that malicious or buggy contestant code cannot interfere with other contestants' sandboxes or the platform's control plane.

#### Acceptance Criteria

1. THE Sandbox Controller SHALL deploy each Submission in a dedicated OCI container with a read-only root filesystem, a non-root user identity (UID ≥ 1000), and Linux seccomp-bpf and AppArmor profiles that deny all syscalls not in the platform-defined syscall allowlist.
2. THE Sandbox Controller SHALL pin each Sandbox container to a dedicated set of CPU cores (minimum 2, maximum 8 as configured per run) using Linux `cpuset` cgroups, with no CPU sharing across concurrent Sandbox containers.
3. THE Sandbox Controller SHALL enforce a hard memory limit of 4 GB per Sandbox container using cgroup v2 `memory.max`, terminating the container if the limit is exceeded.
4. THE Sandbox Controller SHALL restrict each Sandbox container's network access to a private isolated overlay network reachable only by the Bot Fleet Manager and Telemetry Ingester assigned to that Benchmark Run; IF the isolated network cannot be created before Sandbox start, THE Sandbox Controller SHALL abort the deployment and mark the Benchmark Run status as `NETWORK_SETUP_FAILED`.
5. THE Sandbox Controller SHALL limit each Sandbox container's filesystem writes to a dedicated ephemeral tmpfs volume of at most 1 GB.
6. WHEN a Sandbox container terminates unexpectedly during a Benchmark Run, THE Sandbox Controller SHALL record the termination reason and exit code, mark the Benchmark Run status as `SANDBOX_CRASH`, and release all reserved resources (CPU cores, memory cgroup, network namespace, tmpfs volume) within 30 seconds.
7. WHEN a Sandbox reaches its maximum lifetime of 2 hours, THE Sandbox Controller SHALL gracefully stop the container and trigger final metric collection, completing metric collection within 60 seconds; IF metric collection does not complete within 60 seconds, THE Sandbox Controller SHALL mark the run as `COLLECTION_TIMEOUT` and release all resources.
8. WHERE a contestant Submission exposes a health-check endpoint, THE Sandbox Controller SHALL poll the health-check endpoint at 5-second intervals; WHEN 3 consecutive health-check polls fail, THE Sandbox Controller SHALL mark the Sandbox as `UNHEALTHY` and mark the associated Benchmark Run as `FAILED`.
9. THE Sandbox Controller SHALL guarantee that no two active Sandbox containers share cgroup namespaces, network namespaces, or PID namespaces.

---

### Requirement 4: Submission Endpoint Exposure

**User Story:** As the Bot Fleet Manager, I want each sandboxed submission to be reachable at well-known internal endpoints, so that Synthetic Trading Bots can connect using FIX, REST, or WebSocket protocols without manual configuration.

#### Acceptance Criteria

1. THE Sandbox Controller SHALL expose each Sandbox on a dynamically assigned internal IP, publishing FIX acceptor on TCP port 9898, REST API on TCP port 8080, and WebSocket on TCP port 8081 within the Sandbox's isolated network.
2. WHEN a Sandbox reaches `RUNNING` state, THE Sandbox Controller SHALL register the Sandbox's internal endpoints in the Control Plane's service registry within 5 seconds.
3. THE Sandbox Controller SHALL validate that each declared endpoint accepts a TCP connection within 15 seconds of registration.
4. IF a TCP connection to a declared endpoint is refused within the 15-second validation window, THEN THE Sandbox Controller SHALL mark that endpoint as `UNAVAILABLE`, log the failure with the Submission ID, Benchmark Run ID, endpoint protocol, and timestamp, and exclude the endpoint from the set returned by `GetSandboxEndpoints`.
5. WHEN the Bot Fleet Manager calls `GetSandboxEndpoints`, THE Sandbox Controller SHALL return endpoint metadata (internal IP, port, protocol, Submission ID, Benchmark Run ID) only for endpoints whose status is not `UNAVAILABLE`.
6. IF a contestant Submission does not expose any of the three supported protocols (all endpoints are `UNAVAILABLE` or none are registered), THEN THE Sandbox Controller SHALL mark the Benchmark Run status as `NO_ENDPOINTS`, and THE Bot Fleet Manager SHALL not create any bot allocations for that run.
7. WHEN `GetSandboxEndpoints` is called for a Sandbox that has not yet reached `RUNNING` state, THE Sandbox Controller SHALL return a gRPC `NOT_READY` status code rather than an empty endpoint list.

---

### Requirement 5: Distributed Bot Fleet Provisioning

**User Story:** As a platform operator, I want to dynamically spawn thousands of Synthetic Trading Bots for each Benchmark Run, so that contestant submissions are evaluated under realistic high-frequency trading load conditions.

#### Acceptance Criteria

1. THE Bot Fleet Manager SHALL provision a configurable number of Synthetic Trading Bots per Benchmark Run, supporting a minimum of 100 and a maximum of 10,000 concurrent bots per run.
2. THE Bot Fleet Manager SHALL distribute bots across available worker nodes such that no worker node hosts more than 500 bots and the difference in bot count between the most-loaded and least-loaded worker nodes does not exceed 10% of the total fleet size.
3. WHEN provisioning is requested, THE Bot Fleet Manager SHALL have all requested bots in `READY` state within 60 seconds for fleet sizes up to 1,000 bots and within 180 seconds for fleet sizes up to 10,000 bots; IF the deadline is missed, THE Bot Fleet Manager SHALL mark the Benchmark Run as `PROVISIONING_TIMEOUT` and release all partially provisioned bots.
4. THE Bot Fleet Manager SHALL assign each bot a Bot Scenario drawn from the active Bot Scenario set, ensuring that across any fleet of 100 or more bots, all defined scenario types are represented with a deviation of at most ±5% from the configured scenario distribution.
5. THE Bot Fleet Manager SHALL support the following Bot Scenario types: `MARKET_MAKER` (continuous two-sided quotes), `AGGRESSIVE_TAKER` (market orders at maximum rate), `CANCEL_SPAMMER` (high-frequency cancel/replace), `MIXED_RETAIL` (randomized order types at moderate rate), and `LATENCY_PROBER` (low-rate single-sided orders for latency measurement).
6. WHEN a Benchmark Run ends or a `STOP` command is issued, THE Bot Fleet Manager SHALL gracefully terminate all bots in the fleet within 30 seconds; any bot that has not terminated after 30 seconds SHALL be forcibly killed and its resources released.
7. IF a worker node becomes unreachable during a Benchmark Run, THE Bot Fleet Manager SHALL redistribute the affected bots to healthy nodes within 45 seconds; IF no healthy node has sufficient capacity to absorb the displaced bots, THE Bot Fleet Manager SHALL mark the Benchmark Run as `INSUFFICIENT_CAPACITY` and terminate the run.

---

### Requirement 6: Trading Message Generation

**User Story:** As a Synthetic Trading Bot, I want to generate realistic FIX, REST, and WebSocket trading messages, so that contestant submissions are tested against traffic that resembles real-world market conditions.

#### Acceptance Criteria

1. WHILE a Synthetic Trading Bot is operating in FIX mode, THE Synthetic Trading Bot SHALL generate Limit Order, Market Order, and Cancel/Replace messages conforming to FIX 4.2.
2. WHILE a Synthetic Trading Bot is operating in REST mode, THE Synthetic Trading Bot SHALL generate order management requests via HTTP POST/DELETE to the REST Endpoint using a JSON body schema documented in the Platform API specification.
3. WHILE a Synthetic Trading Bot is operating in WebSocket mode, THE Synthetic Trading Bot SHALL generate streaming order events over the WebSocket Endpoint using the JSON message schema documented in the Platform API specification.
4. THE Synthetic Trading Bot SHALL generate a `ClOrdID` (FIX tag 11) or equivalent request ID that is unique within the scope of a single Benchmark Run for every outbound message to enable end-to-end correlation.
5. WHEN operating as a `LATENCY_PROBER`, THE Synthetic Trading Bot SHALL send at most 100 messages per second per bot instance, SHALL embed the send timestamp with microsecond precision in the outbound message, and SHALL forward that timestamp to the Telemetry Ingester for latency calculation.
6. WHEN operating as an `AGGRESSIVE_TAKER`, THE Synthetic Trading Bot SHALL sustain a target message rate of at least 10,000 messages per second per bot instance until the Sandbox signals back-pressure (HTTP 429 for REST, TCP send buffer full for longer than 500 ms for FIX, or WebSocket flow-control stall for longer than 500 ms) or the run ends; WHEN back-pressure is detected, THE bot SHALL reduce its rate within 1 second.
7. THE Synthetic Trading Bot SHALL embed a monotonically increasing per-bot-session sequence number (resetting to 1 at session start) and a nanosecond-resolution send timestamp in every message across all three protocol modes.

---

### Requirement 7: Telemetry Collection and Ingestion

**User Story:** As a platform operator, I want granular, low-latency telemetry collected from every Benchmark Run, so that latency percentiles, throughput limits, and correctness metrics are accurate and auditable.

#### Acceptance Criteria

1. THE Telemetry Ingester SHALL receive telemetry events from Synthetic Trading Bots and Sandbox response interceptors via Kafka / Redpanda topics partitioned by Benchmark Run ID.
2. THE Telemetry Ingester SHALL compute p50, p90, and p99 round-trip latency using the send timestamp embedded in each message and the response receipt timestamp recorded by the bot, with a measurement resolution of 1 microsecond, over a rolling 60-second window updated every 5 seconds during a live run and over the full population at run completion.
3. WHILE a Benchmark Run is in `RUNNING` state, THE Telemetry Ingester SHALL compute the rolling maximum TPS sustained by the Sandbox without exceeding a p99 latency of 10 ms over a 5-second sliding window, updated every 5 seconds.
4. THE Telemetry Ingester SHALL persist all raw latency samples and aggregated statistics to TimescaleDB within 500 ms of the event occurring, using hypertables partitioned by `benchmark_run_id` and `event_time`.
5. THE Telemetry Ingester SHALL publish aggregated metric updates (p50/p90/p99 latency, current TPS, error rate) to a Redis pub/sub channel keyed by Benchmark Run ID at a minimum frequency of once per second.
6. WHEN a Sandbox returns an error response (HTTP 4xx/5xx, FIX session-level reject, or WebSocket close frame with error code), THE Telemetry Ingester SHALL record the error event with its timestamp, request ID, and error code to TimescaleDB within 500 ms of the event.
7. THE Telemetry Ingester SHALL buffer up to 100,000 raw telemetry events in memory before flushing to TimescaleDB; IF the buffer reaches 80% capacity, THE Telemetry Ingester SHALL trigger an immediate flush; IF the buffer reaches 100% capacity, THE Telemetry Ingester SHALL pause Kafka partition consumption until the buffer drops below 50% capacity, with zero event drops.
8. WHERE a telemetry event has a valid sequence number, THE Telemetry Ingester SHALL guarantee at-least-once delivery via Kafka consumer group offset management and SHALL deduplicate events by composite key (Benchmark Run ID + Bot ID + sequence number) before metric computation.

---

### Requirement 8: Correctness Validation

**User Story:** As a platform operator, I want the platform to validate price-time priority and fill accuracy for each submission, so that contestants who implement correct matching logic are rewarded appropriately in the scoring model.

#### Acceptance Criteria

1. THE Telemetry Ingester SHALL compare every fill event returned by a Sandbox against the expected fill computed by the Platform's Reference Matching Engine, using identical order book state and the same sequence of input orders.
2. THE Telemetry Ingester SHALL flag a fill as a `PRICE_PRIORITY_VIOLATION` when a Sandbox assigns a fill at a worse price than what the Reference Matching Engine computed for the same order book state.
3. THE Telemetry Ingester SHALL flag a fill as a `TIME_PRIORITY_VIOLATION` when a Sandbox fills a later-arriving order at the same price level before an earlier-arriving order.
4. THE Telemetry Ingester SHALL flag a fill as a `QUANTITY_MISMATCH` when the filled quantity reported by the Sandbox differs from the expected quantity by more than zero shares.
5. THE Telemetry Ingester SHALL compute a Fill Accuracy Score as the ratio of correct fills to total fills over the Benchmark Run, expressed as a value in the range [0.0, 1.0]; a fill is correct if and only if it has no associated `PRICE_PRIORITY_VIOLATION`, `TIME_PRIORITY_VIOLATION`, or `QUANTITY_MISMATCH` flag.
6. THE Platform SHALL include a Reference Matching Engine implementing price-time priority as a deterministic, side-effect-free function that accepts an ordered sequence of order events and returns the expected set of fill events.
7. WHEN the Reference Matching Engine is given the same ordered input sequence multiple times, THE Reference Matching Engine SHALL produce identical output fill sequences (determinism invariant).
8. FOR ALL valid order sequences, applying the Reference Matching Engine and then re-running it with the output fills appended as cancellations SHALL leave the order book in a consistent empty or partially-filled state (idempotency under cancellation).

---

### Requirement 9: Composite Scoring

**User Story:** As a contestant, I want my submission scored on a transparent composite metric that rewards speed, stability, and correctness, so that I understand what the ranking reflects and can optimize accordingly.

#### Acceptance Criteria

1. THE Leaderboard Service SHALL compute a Composite Score for each completed Benchmark Run using the formula: `Score = (w_speed × SpeedScore) + (w_stability × StabilityScore) + (w_accuracy × AccuracyScore)`, where the default weights are `w_speed = 0.35`, `w_stability = 0.35`, `w_accuracy = 0.30`, and `w_speed + w_stability + w_accuracy = 1.0`.
2. THE Leaderboard Service SHALL compute SpeedScore as a normalized value in [0.0, 1.0] derived from the p99 latency using `SpeedScore = clamp((100 - p99_ms) / 99, 0.0, 1.0)`, where a p99 latency of ≤ 1 ms maps to 1.0 and a p99 latency of ≥ 100 ms maps to 0.0.
3. THE Leaderboard Service SHALL compute StabilityScore as a normalized value in [0.0, 1.0] derived from the maximum sustained TPS using `StabilityScore = clamp(max_tps / 1_000_000, 0.0, 1.0)`, where a TPS of ≥ 1,000,000 maps to 1.0 and a TPS of 0 maps to 0.0.
4. THE Leaderboard Service SHALL compute AccuracyScore as the Fill Accuracy Score produced by the Telemetry Ingester for the same Benchmark Run; IF the Telemetry Ingester score is unavailable, THE Leaderboard Service SHALL use 0.0 as the AccuracyScore.
5. WHEN the Leaderboard Service receives an updated metric batch from the Telemetry Ingester, THE Leaderboard Service SHALL recompute the Composite Score within 2 seconds and publish the updated score to Redis.
6. IF a Benchmark Run ends with any terminal failure status (including `SANDBOX_CRASH`, `BUILD_FAILED`, `BUILD_TIMEOUT`, `NO_ENDPOINTS`, `PROVISIONING_TIMEOUT`, or `INSUFFICIENT_CAPACITY`), THEN THE Leaderboard Service SHALL assign a Composite Score of 0.0 for that run.
7. THE Leaderboard Service SHALL retain the historical Composite Scores for all Benchmark Runs per contestant, and use the maximum Composite Score across all runs as the contestant's official leaderboard standing.
8. IF Redis is unavailable when THE Leaderboard Service attempts to publish an updated score, THE Leaderboard Service SHALL retry the publish up to 3 times with exponential backoff starting at 100 ms before logging the failure and continuing.

---

### Requirement 10: Real-Time Leaderboard and Analytics Frontend

**User Story:** As a hackathon spectator or contestant, I want to see a live-updating leaderboard and per-submission analytics, so that I can follow the competition standings and understand submission performance in real time.

#### Acceptance Criteria

1. WHEN the Leaderboard Frontend receives a score update event from the Leaderboard Service, THE Leaderboard Frontend SHALL update the displayed ranked standings within 2 seconds of the event's arrival at the browser.
2. THE Leaderboard Frontend SHALL connect to the Platform via a WebSocket connection and receive score update events pushed by the Leaderboard Service without requiring page refresh.
3. THE Leaderboard Frontend SHALL display for each contestant: rank, contestant handle, Composite Score, SpeedScore, StabilityScore, AccuracyScore, p99 latency (ms), maximum TPS, Fill Accuracy percentage, and Benchmark Run status.
4. WHEN a contestant is selected, THE Leaderboard Frontend SHALL render a live time-series chart of p50/p90/p99 latency and TPS for that contestant's most recent Benchmark Run, updating at a minimum frequency of once per second; IF no Benchmark Run exists for the selected contestant, THE Leaderboard Frontend SHALL display an empty chart with a "No data available" indicator.
5. IF the WebSocket connection is interrupted, THE Leaderboard Frontend SHALL continue displaying the last-known data and SHALL automatically attempt reconnection with exponential backoff starting at 1 second and capping at 30 seconds; WHEN the connection is re-established, THE Leaderboard Frontend SHALL request a full state snapshot to recover any updates missed during the disconnection.
6. THE Leaderboard Frontend SHALL support a minimum of 500 simultaneous browser clients without degrading the push latency beyond 2 seconds.
7. WHERE a Benchmark Run is in `IN_PROGRESS` status, THE Leaderboard Frontend SHALL display a live progress indicator showing elapsed time since Benchmark Run creation, current TPS, and the most recently computed p99 latency.
8. WHEN the Leaderboard Frontend is loaded for the first time or after a full page reload, THE Leaderboard Frontend SHALL fetch the current complete leaderboard state via an HTTP REST endpoint before establishing the WebSocket connection, so that initial rankings are displayed without waiting for the first push event.

---

### Requirement 11: Control Plane and Orchestration API

**User Story:** As a platform operator, I want a central API to manage benchmark runs, contestants, and platform resources, so that the competition can be administered programmatically and at scale.

#### Acceptance Criteria

1. THE Control Plane SHALL expose a gRPC API with the following services: `ContestantService` (registration, token management), `SubmissionService` (upload status, build logs), `BenchmarkService` (run lifecycle management), and `LeaderboardService` (score queries).
2. THE Control Plane SHALL authenticate all gRPC calls using mTLS certificates issued by a platform-managed Certificate Authority, with separate certificate pools for contestant clients, operator clients, and internal service-to-service communication.
3. WHEN a Benchmark Run is created via `BenchmarkService.CreateRun`, THE Control Plane SHALL transition the run through the states `QUEUED → BUILDING → DEPLOYING → RUNNING → COLLECTING → SCORING → COMPLETE` in order; each state transition SHALL be recorded in a persistent audit trail that survives process restart, with each entry containing the timestamp, Benchmark Run ID, prior state, new state, and actor identity; terminal failure states (`BUILD_FAILED`, `SANDBOX_CRASH`, etc.) SHALL also be recorded in the audit trail.
4. THE Control Plane SHALL enforce a maximum of 3 concurrent Benchmark Runs per contestant at any time, where "concurrent" means any run in a non-terminal state; IF a fourth run is requested, THE Control Plane SHALL return a `RESOURCE_EXHAUSTED` gRPC status.
5. THE Control Plane SHALL expose a Prometheus-compatible `/metrics` endpoint reporting: active Sandbox count, active bot count, Kafka consumer lag per topic, TimescaleDB write latency p99, and Control Plane request rate.
6. THE Leaderboard Service gRPC endpoint SHALL return paginated leaderboard results with a p99 response time under 50 ms for pages of up to 100 contestants.
7. WHEN a Benchmark Run state transition fails (e.g., the Sandbox Controller returns an error during `DEPLOYING`), THE Control Plane SHALL transition the run to a terminal `FAILED` state, record the failure reason in the audit trail, and return an appropriate gRPC error status to the caller.

---

### Requirement 12: Infrastructure as Code and Deployment

**User Story:** As a platform operator, I want the entire platform provisioned via declarative Infrastructure as Code, so that the environment is reproducible, auditable, and can be torn down and redeployed within 30 minutes.

#### Acceptance Criteria

1. THE Platform SHALL provide Terraform modules that provision all required cloud resources (compute nodes, networking, managed Kafka / Redpanda cluster, TimescaleDB instance, Redis cluster, container registry) without requiring any action outside the IaC toolchain CLI; re-applying the same modules to an already-provisioned environment SHALL produce no resource replacements or configuration drift.
2. THE Platform SHALL provide Kubernetes manifests or Helm charts for all Platform microservices (Submission Engine, Sandbox Controller, Bot Fleet Manager, Telemetry Ingester, Leaderboard Service, Leaderboard Frontend, Control Plane API) deployable to a Kubernetes cluster of version 1.29 or later.
3. THE Platform IaC SHALL configure all inter-service network policies to deny traffic by default and permit only explicitly declared ingress and egress paths.
4. THE Platform IaC SHALL configure resource requests and limits for every Kubernetes workload, with no workload permitted to run without declared CPU and memory limits.
5. WHEN the IaC is applied to a fresh environment, THE Platform SHALL reach a fully operational state — defined as all Kubernetes workload readiness probes for all Platform microservices returning healthy — within 30 minutes of the IaC toolchain CLI invocation completing.
6. THE Platform IaC SHALL include a `destroy` target that removes all provisioned resources, including persistent volumes, DNS records, and unattached IP addresses allocated during provisioning, with no orphaned cloud objects remaining after the target completes successfully.
7. THE Platform SHALL store all secrets (TLS private keys, database credentials, API tokens) in a dedicated secrets manager (Kubernetes Secrets encrypted at rest with a KMS provider or an external vault), with no secrets hard-coded in IaC source files or container images.
8. THE Platform IaC SHALL configure each microservice's access to the secrets manager such that each microservice is granted read access only to the secrets it requires to operate, with no microservice granted access to secrets belonging to other microservices.

---

### Requirement 13: Platform Observability and Resilience

**User Story:** As a platform operator, I want comprehensive observability and automatic recovery from partial failures, so that the competition continues uninterrupted even when individual components degrade.

#### Acceptance Criteria

1. THE Platform SHALL export distributed traces for all cross-service request paths using OpenTelemetry, with trace context propagated through gRPC metadata and Kafka message headers.
2. THE Control Plane SHALL expose structured JSON logs for all platform events at severity levels `DEBUG`, `INFO`, `WARN`, and `ERROR`, routed to a centralized log aggregation endpoint.
3. WHEN the Telemetry Ingester falls behind Kafka consumer lag by more than 50,000 events, THE Platform SHALL emit a `HIGH_LAG` alert to the configured alerting endpoint (PagerDuty webhook or equivalent).
4. WHEN a Sandbox Controller node becomes unavailable (its Kubernetes readiness probe fails), THE Control Plane SHALL reschedule the affected Sandbox deployments to healthy nodes within 60 seconds using Kubernetes pod disruption budgets and node affinity rules.
5. WHEN the Redis leaderboard cache becomes unavailable, THE Leaderboard Service SHALL fall back to querying TimescaleDB for score data.
6. WHILE the Redis leaderboard cache is unavailable, THE Leaderboard Service SHALL serve requests with a latency degradation of at most 500 ms above the p99 measured during the most recent 5-minute window of normal Redis-backed operation.
7. WHEN a deployment completes, THE Platform SHALL execute an automated end-to-end smoke test using a synthetic submission, verifying the complete pipeline from upload to leaderboard score update within 120 seconds; IF the smoke test fails, THE Platform SHALL emit an alert to the configured alerting endpoint.
8. THE Platform SHALL retain all raw telemetry data in TimescaleDB for a minimum of 30 days, with automated compression applied to data older than 7 days using TimescaleDB native compression.
