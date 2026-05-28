FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY libs/ libs/
COPY services/build-pipeline/ services/build-pipeline/

WORKDIR /workspace/services/build-pipeline
RUN GOWORK=off go mod download
RUN GOWORK=off CGO_ENABLED=0 GOOS=linux go build -trimpath -o /build-pipeline .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build-pipeline /usr/local/bin/build-pipeline

RUN useradd -u 1001 -m appuser
USER appuser

ENTRYPOINT ["build-pipeline"]
