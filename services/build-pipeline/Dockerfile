# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

COPY go.work go.work.sum ./
COPY libs/shared-go/ libs/shared-go/
COPY services/build-pipeline/ services/build-pipeline/

RUN go work sync

WORKDIR /workspace/services/build-pipeline
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /build-pipeline .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build-pipeline /usr/local/bin/build-pipeline

RUN useradd -u 1001 -m appuser
USER appuser

ENTRYPOINT ["build-pipeline"]
