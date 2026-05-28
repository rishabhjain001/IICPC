FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

# Copy shared library and service source (no go.work - standalone build)
COPY libs/ libs/
COPY services/sandbox-controller/ services/sandbox-controller/

WORKDIR /workspace/services/sandbox-controller
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /sandbox-controller .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /sandbox-controller /usr/local/bin/sandbox-controller

RUN useradd -u 1001 -m appuser
USER appuser
ENTRYPOINT ["sandbox-controller"]
