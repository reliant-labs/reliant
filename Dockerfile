# Dockerfile — single-stage build for all Reliant services.
#
# Produces ONE image. The subcommand is provided at runtime:
#
#   docker run reliant server api
#   docker run reliant server worker
#   docker run reliant server gateway
#   docker run reliant daemon start
#
# Ports and configuration are set via environment variables or flags.
# For tools-daemon (needs root for FS access), override the user at
# deploy time: `user: root` in compose or securityContext in k8s.

# ── Build ────────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine AS builder
ARG TARGETARCH
RUN apk add --no-cache git
WORKDIR /app
# go.work in the repo root references the sibling ../forge checkout, which is
# absent from the Docker build context. Disable workspace mode so the build
# resolves the pinned forge module from go.mod (matches a clean CI build).
ENV GOWORK=off
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOARCH=${TARGETARCH} go build -o /reliant ./cmd/reliant/

# ── Runtime ──────────────────────────────────────────────────────────────────
# Runtime base is debian (glibc), NOT alpine (musl): since adopting the forge
# module (-> kcl-lang.io -> ebitengine/purego), the binary carries
# cgo_import_dynamic directives and is dynamically linked against glibc's
# ld-linux even with CGO_ENABLED=0, so it cannot exec on musl ("no such file or
# directory" crashloop). bookworm-slim stays small while providing glibc.
FROM debian:bookworm-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata git && \
    rm -rf /var/lib/apt/lists/* && \
    groupadd -g 65532 nonroot && \
    useradd -u 65532 -g 65532 -s /usr/sbin/nologin -M nonroot && \
    mkdir -p /data && chown nonroot:nonroot /data
COPY --from=builder /reliant /usr/local/bin/reliant
USER 65532:65532
ENTRYPOINT ["reliant"]