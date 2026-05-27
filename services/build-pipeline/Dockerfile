FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY go.work go.work.sum ./
COPY libs/ libs/
COPY services/build-pipeline/ services/build-pipeline/

# Stub out other service directories so go.work doesn't fail
COPY services/submission-engine/go.mod services/submission-engine/go.mod
COPY services/submission-engine/go.sum services/submission-engine/go.sum
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

WORKDIR /workspace/services/build-pipeline
RUN GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux go build -trimpath -o /build-pipeline .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build-pipeline /usr/local/bin/build-pipeline

RUN useradd -u 1001 -m appuser
USER appuser

ENTRYPOINT ["build-pipeline"]