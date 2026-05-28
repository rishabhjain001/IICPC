FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

# Copy shared library and service source (no go.work - standalone build)
COPY libs/ libs/
COPY services/telemetry-ingester/ services/telemetry-ingester/

WORKDIR /workspace/services/telemetry-ingester
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /telemetry-ingester .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /telemetry-ingester /usr/local/bin/telemetry-ingester

RUN useradd -u 1001 -m appuser
USER appuser
ENTRYPOINT ["telemetry-ingester"]
