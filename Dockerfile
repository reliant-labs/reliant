# Dockerfile — single-stage build for all Reliant services.
#
# Produces ONE image. The subcommand is provided at runtime:
#
#   docker run reliant server api
#   docker run reliant server worker
#   docker run reliant server gateway
#   docker run reliant daemon start
#   docker run reliant monolith
#
# Ports and configuration are set via environment variables or flags.
# For tools-daemon (needs root for FS access), override the user at
# deploy time: `user: root` in compose or securityContext in k8s.

# ── Build ────────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 go build -o /reliant ./cmd/reliant/

# ── Runtime ──────────────────────────────────────────────────────────────────
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata git && \
    addgroup -g 65532 -S nonroot && \
    adduser -u 65532 -S nonroot -G nonroot && \
    mkdir -p /data && chown nonroot:nonroot /data
COPY --from=builder /reliant /usr/local/bin/reliant
USER 65532:65532
ENTRYPOINT ["reliant"]
