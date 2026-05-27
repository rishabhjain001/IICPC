# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY go.work go.work.sum ./
COPY libs/shared-go/ libs/shared-go/
COPY services/telemetry-ingester/ services/telemetry-ingester/

RUN go work sync

WORKDIR /workspace/services/telemetry-ingester
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /telemetry-ingester .

# ── Runtime stage ─────────────────────────────────────────────────────────────
# CGO_ENABLED=0 ensures we don't need the Rust .so at build time; it's
# mounted at runtime via the docker-compose volume: ./libs/rme -> /rme
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /telemetry-ingester /usr/local/bin/telemetry-ingester

RUN useradd -u 1001 -m appuser
USER appuser

ENTRYPOINT ["telemetry-ingester"]
