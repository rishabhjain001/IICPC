FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

# Copy shared library and service source (no go.work - standalone build)
COPY libs/ libs/
COPY services/control-plane-api/ services/control-plane-api/

WORKDIR /workspace/services/control-plane-api
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /control-plane-api .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /control-plane-api /usr/local/bin/control-plane-api

RUN useradd -u 1001 -m appuser
USER appuser
EXPOSE 9090
EXPOSE 9091
ENTRYPOINT ["control-plane-api"]
