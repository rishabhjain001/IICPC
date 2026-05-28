FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY libs/ libs/
COPY services/submission-engine/ services/submission-engine/

WORKDIR /workspace/services/submission-engine
RUN GOWORK=off go mod download
RUN GOWORK=off CGO_ENABLED=0 GOOS=linux go build -trimpath -o /submission-engine .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /submission-engine /usr/local/bin/submission-engine

RUN useradd -u 1001 -m appuser
USER appuser
EXPOSE 8443
ENTRYPOINT ["submission-engine"]
