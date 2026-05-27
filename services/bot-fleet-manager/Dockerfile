# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY go.work go.work.sum ./
COPY libs/shared-go/ libs/shared-go/
COPY services/bot-fleet-manager/ services/bot-fleet-manager/

RUN go work sync

WORKDIR /workspace/services/bot-fleet-manager
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bot-fleet-manager .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bot-fleet-manager /usr/local/bin/bot-fleet-manager

RUN useradd -u 1001 -m appuser
USER appuser

ENTRYPOINT ["bot-fleet-manager"]
