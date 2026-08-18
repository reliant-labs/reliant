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
# golang:1.26.2-alpine — pinned by digest, pulled via the GCP Artifact Registry
# pull-through cache (dodges Docker Hub's per-IP rate limit).
FROM --platform=$BUILDPLATFORM us-docker.pkg.dev/reliant-nonprod-490701/dockerhub/library/golang@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS builder
ARG TARGETARCH
RUN apk add --no-cache git
WORKDIR /app
# go.work (gitignored, machine-local) bridges this build to the sibling
# ../forge checkout for local development. That checkout is absent from the
# Docker build context, so disable workspace mode: the build then resolves the
# forge modules at the VERSION PINNED IN go.mod, from the module proxy.
#
# This only works because go.mod carries no `replace` for forge. A replace
# directive applies in every mode — GOWORK=off does NOT disable it — so one
# would make this build fail with "replacement directory ../forge/pkg does not
# exist" no matter what this line says. Keep forge out of the replace block.
ENV GOWORK=off
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# VERSION stamps the binary so `reliant version` reports the release rather
# than "unknown". The release workflow already injects internal/version for the
# Electron-bundled binary; this image built without it, so every published
# container reported an unknown version. Defaults to "dev" for a plain
# `docker build` with no --build-arg.
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOARCH=${TARGETARCH} go build \
      -ldflags "-X github.com/reliant-labs/reliant/internal/version.Version=${VERSION}" \
      -o /reliant ./cmd/reliant/

# ── Runtime ──────────────────────────────────────────────────────────────────
# Runtime base is debian (glibc), NOT alpine (musl): since adopting the forge
# module (-> kcl-lang.io -> ebitengine/purego), the binary carries
# cgo_import_dynamic directives and is dynamically linked against glibc's
# ld-linux even with CGO_ENABLED=0, so it cannot exec on musl ("no such file or
# directory" crashloop). bookworm-slim stays small while providing glibc.
# debian:bookworm-slim — pinned by digest, pulled via the GCP Artifact Registry
# pull-through cache (dodges Docker Hub's per-IP rate limit).
FROM us-docker.pkg.dev/reliant-nonprod-490701/dockerhub/library/debian@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata git && \
    rm -rf /var/lib/apt/lists/* && \
    groupadd -g 65532 nonroot && \
    useradd -u 65532 -g 65532 -s /usr/sbin/nologin -M nonroot && \
    mkdir -p /data && chown nonroot:nonroot /data
COPY --from=builder /reliant /usr/local/bin/reliant
USER 65532:65532
ENTRYPOINT ["reliant"]