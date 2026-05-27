FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY go.work go.work.sum ./
COPY libs/ libs/
COPY services/ services/

WORKDIR /workspace/services/leaderboard-service
RUN GOWORK=off GOFLAGS=-mod=mod CGO_ENABLED=0 GOOS=linux go build -trimpath -o /leaderboard-service .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /leaderboard-service /usr/local/bin/leaderboard-service

RUN useradd -u 1001 -m appuser
USER appuser
EXPOSE 8082
ENTRYPOINT ["leaderboard-service"]
