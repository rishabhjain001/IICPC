FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

# Workspace descriptor
COPY go.work go.work.sum ./

# Shared library
COPY libs/ libs/

# Target service
COPY services/submission-engine/ services/submission-engine/

# Stub go.mod/go.sum for remaining workspace members
COPY services/build-pipeline/go.mod services/build-pipeline/go.mod
COPY services/build-pipeline/go.sum services/build-pipeline/go.sum
COPY services/sandbox-controller/go.mod services/sandbox-controller/go.mod
COPY services/sandbox-controller/go.sum services/sandbox-controller/go.sum
COPY services/bot-fleet-manager/go.mod services/bot-fleet-manager/go.mod
COPY services/bot-fleet-manager/go.sum services/bot-fleet-manager/go.sum
COPY services/telemetry-ingester/go.mod services/telemetry-ingester/go.mod
COPY services/telemetry-ingester/go.sum services/telemetry-ingester/go.sum
COPY services/leaderboard-service/go.mod services/leaderboard-service/go.mod
COPY services/leaderboard-service/go.sum services/leaderboard-service/go.sum
COPY services/control-plane-api/go.mod services/control-plane-api/go.mod
COPY services/control-plane-api/go.sum services/control-plane-api/go.sum

WORKDIR /workspace/services/submission-engine
RUN GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux go build -trimpath -o /submission-engine .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /submission-engine /usr/local/bin/submission-engine

RUN useradd -u 1001 -m appuser
USER appuser
EXPOSE 8443
ENTRYPOINT ["submission-engine"]
