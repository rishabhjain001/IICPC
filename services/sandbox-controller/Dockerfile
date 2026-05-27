# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY go.work go.work.sum ./
COPY libs/shared-go/ libs/shared-go/
COPY services/sandbox-controller/ services/sandbox-controller/

RUN go work sync

WORKDIR /workspace/services/sandbox-controller
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /sandbox-controller .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /sandbox-controller /usr/local/bin/sandbox-controller

RUN useradd -u 1001 -m appuser
USER appuser

ENTRYPOINT ["sandbox-controller"]
