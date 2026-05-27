# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY go.work go.work.sum ./
COPY libs/shared-go/ libs/shared-go/
COPY services/leaderboard-service/ services/leaderboard-service/

RUN go work sync

WORKDIR /workspace/services/leaderboard-service
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /leaderboard-service .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /leaderboard-service /usr/local/bin/leaderboard-service

RUN useradd -u 1001 -m appuser
USER appuser

EXPOSE 8082

ENTRYPOINT ["leaderboard-service"]
