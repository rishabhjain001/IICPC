FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

# Workspace descriptor
COPY go.work go.work.sum ./

# Shared library
COPY libs/ libs/

# Target service
COPY services/sandbox-controller/ services/sandbox-controller/

# Stub go.mod/go.sum for remaining workspace members
COPY services/submission-engine/go.mod services/submission-engine/go.mod
COPY services/submission-engine/go.sum services/submission-engine/go.sum
COPY services/build-pipeline/go.mod services/build-pipeline/go.mod
COPY services/build-pipeline/go.sum services/build-pipeline/go.sum
COPY services/bot-fleet-manager/go.mod services/bot-fleet-manager/go.mod
COPY services/bot-fleet-manager/go.sum services/bot-fleet-manager/go.sum
COPY services/telemetry-ingester/go.mod services/telemetry-ingester/go.mod
COPY services/telemetry-ingester/go.sum services/telemetry-ingester/go.sum
COPY services/leaderboard-service/go.mod services/leaderboard-service/go.mod
COPY services/leaderboard-service/go.sum services/leaderboard-service/go.sum
COPY services/control-plane-api/go.mod services/control-plane-api/go.mod
COPY services/control-plane-api/go.sum services/control-plane-api/go.sum

WORKDIR /workspace/services/sandbox-controller
RUN GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux go build -trimpath -o /sandbox-controller .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /sandbox-controller /usr/local/bin/sandbox-controller

RUN useradd -u 1001 -m appuser
USER appuser
ENTRYPOINT ["sandbox-controller"]
