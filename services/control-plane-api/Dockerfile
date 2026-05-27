# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY go.work go.work.sum ./
COPY libs/shared-go/ libs/shared-go/
COPY services/control-plane-api/ services/control-plane-api/

RUN go work sync

WORKDIR /workspace/services/control-plane-api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /control-plane-api .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /control-plane-api /usr/local/bin/control-plane-api

RUN useradd -u 1001 -m appuser
USER appuser

EXPOSE 9090 9091

ENTRYPOINT ["control-plane-api"]
