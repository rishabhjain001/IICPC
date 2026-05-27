# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS builder

WORKDIR /workspace

# Copy the go workspace and all module roots that the service depends on.
COPY go.work go.work.sum ./
COPY libs/shared-go/ libs/shared-go/
COPY services/submission-engine/ services/submission-engine/

# Download dependencies inside the workspace context.
RUN go work sync

WORKDIR /workspace/services/submission-engine
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /submission-engine .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /submission-engine /usr/local/bin/submission-engine

# Non-root user
RUN useradd -u 1001 -m appuser
USER appuser

EXPOSE 8443

ENTRYPOINT ["submission-engine"]
