# syntax=docker/dockerfile:1.7
# deploy/gate/Dockerfile: the merge queue's gate image, one for forge,
# control-plane and reliant.
#
# A gate machine's daemon runs every gate in this image
# (DaemonConfig gate_image / RELIANT_DAEMON_GATE_IMAGE). Today that is the
# runc backend (internal/mergequeue/gaterun/runcgate), which runs the
# container with:
#   - a read-only root filesystem, with tmpfs at /tmp (1g) and /run;
#   - a per-gate work volume at /gate (gaterun.WorkMount) that holds the tree
#     in /gate/src;
#   - optionally, a per-repository warm cache volume at /gate/cache
#     (gaterun.CacheMount);
#   - optionally, the machine's trusted Go build cache, read-only, at
#     /gate/trusted-go-build (gaterun.TrustedGoCacheMount; below);
#   - every capability dropped and no docker engine;
#   - an environment that is exactly gaterun.GateEnv, plus the ENV declared
#     here. The post-create check accepts the image's own variables.
#
# So everything a toolchain writes goes under /gate, never the root:
#   /gate/home   HOME (GateEnv sets the same), XDG config, data and state;
#   /gate/cache  GOCACHE, GOMODCACHE, the pnpm store, the npm and corepack
#                caches, XDG_CACHE_HOME (forge's embedded Postgres, electron),
#                and Playwright's browsers dir.
# The image creates /gate and its subdirectories, owned by the gate user. An
# empty volume mounted there is seeded from them by docker, so the work volume
# and a fresh cache volume both start writable by that user. Without a cache
# volume, /gate/cache is just a directory on the per-gate work volume: it is
# disk-backed, unlike the 1g /tmp, and it goes with the gate's teardown.
#
# One Go build cache per machine. The machine's trusted tier
# (/var/cache/workspace/go-build-trusted, written only by main's seeding) is
# mounted read-only at /gate/trusted-go-build, and every go command here runs
# with GOCACHEPROG="forge cache prog --trusted /gate/trusted-go-build": it
# reads the trusted tier first, then the overlay, and writes only to the
# overlay. The overlay is GOCACHE (/gate/cache/go-build: the warm cache volume
# when the gate has one, else the per-gate work volume). When nothing is
# mounted at /gate/trusted-go-build (a machine with no trusted tier, or
# gate_trusted_go_cache=off) the prog runs on the overlay alone; a missing
# trusted tier never fails a build.
#
# A trusted entry is reused only when the action ID matches the hosts', so
# the final stage is built to match them (cicd and ovh-build-1, Ubuntu 24.04):
#   - Ubuntu 24.04 with noble's stock `gcc`. cmd/go folds the C compiler's
#     version line into the action ID of every cgo package (runtime/cgo, net,
#     os/user) and so of everything that imports them; under bookworm's gcc
#     12.2.0 no cgo-reaching package ever shared an entry with the hosts.
#     After a rebuild, `gcc --version` here must print the hosts' line
#     (13.3.0-6ubuntu2~24.04.1 on 2026-11-27), or the cgo half misses.
#   - GOFLAGS=-trimpath, as the agents, the Taskfiles and the runners set it:
#     -trimpath changes every action ID. A repository's gate.env that sets
#     GOFLAGS replaces this one, so it must keep -trimpath.
#   - The same go (1.27.4), GOAMD64=v1 (the default), CGO_ENABLED=1 and an
#     empty GOEXPERIMENT. GOROOT's location does not matter.
#
# The tool versions are the ones the three repos pin. A bump here is a rebuild:
#   go 1.27.4         go.mod `toolchain` (all three repos); go.dev's official
#                     linux-amd64 tarball, checked against its sha256
#   gcc               noble's stock gcc (see above), plus g++, libc6-dev and
#                     pkg-config (cgo, which -race needs)
#   git 2.43          noble's (reliant's gitport needs >= 2.40 for
#                     merge-tree --write-tree --merge-base)
#   forge             FORGE_VERSION (below): a forge with `forge cache prog`
#   node 24           reliant .nvmrc; engines in all three are >=22 <27
#   pnpm 12.15.0      packageManager (all three repos)
#   task 3.54.0       control-plane scripts/tool-versions.env TASK_VERSION
#   buf 1.57.0        reliant and control-plane CI (bufbuild/buf-action version)
#   golangci-lint     v2.14.0: control-plane GOLANGCI_LINT_VERSION, forge
#                     templates.GolangciLintVersion. Built from source with this
#                     go, because a binary built with an older go cannot lint a
#                     go1.27 module.
#   kcl 0.13.2        control-plane KCL_VERSION (ci:full's renders)
#   yq v4.54.3        control-plane YQ_VERSION
#   playwright 1.58.0 reliant frontends/web pnpm-lock.yaml (@playwright/test),
#                     with chromium and webkit plus their system packages
#                     (task test:web:e2e)
#
# Build it on the gate machine and tag it twice, once by date and commit and
# once with a stable tag that the daemon config names:
#   tag=$(date -u +%Y%m%d)-$(git rev-parse --short HEAD)
#   docker build -t reliant-gate:$tag -t reliant-gate:current deploy/gate
# Smoke test, run the way a gate runs it: a read-only root and a fresh volume
# at /gate. Without that volume, /gate is part of the read-only root, and kcl,
# go and pnpm cannot write their caches.
#   docker volume create gate-smoke
#   docker run --rm --read-only --tmpfs /tmp --cap-drop ALL \
#     --security-opt no-new-privileges --mount source=gate-smoke,target=/gate \
#     reliant-gate:current sh -c 'go version && task --version && pnpm --version \
#       && forge --version && gcc --version && git --version'
#   docker volume rm gate-smoke
#
# The runc backend copies the tree in with `docker cp -a`, so it is owned by
# the gate user, not by the uid that fetched it on the host.
#
# Known gaps (pipeline redesign §3.3): there is no docker engine here, so a
# test that needs one skips with "skip: docker unavailable (gate)", or fails
# when its repo's helper turns a missing engine into a failure under CI. A
# dedicated gate machine with sysbox (a nested engine per gate) closes this gap
# and the isolation gap together.

FROM golang:1.27.4-bookworm AS gotools
# golangci-lint built with the gate's go (see the version table above).
RUN GOBIN=/out GOFLAGS=-trimpath go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

FROM golang:1.27.4-bookworm AS forgebuild
# FORGE_VERSION must be a forge with `forge cache prog` (forge#3076 and later).
# Every go command in the gate runs through it (GOCACHEPROG, below), so a
# forge without it fails every Go build.
ARG FORGE_VERSION=v1.13.1-0.20261127154748-0b29fb6eba25
RUN GOBIN=/out GOFLAGS=-trimpath go install "github.com/reliant-labs/forge/cmd/forge@${FORGE_VERSION}"

FROM node:24-bookworm-slim AS node

FROM ubuntu:24.04

ARG GO_VERSION=1.27.4
ARG GO_SHA256=7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840
ARG TASK_VERSION=3.54.0
ARG BUF_VERSION=1.57.0
ARG KCL_VERSION=0.13.2
ARG YQ_VERSION=v4.54.3
ARG PNPM_VERSION=12.15.0
ARG PLAYWRIGHT_VERSION=1.58.0
ARG GATE_UID=10001
ARG DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# The gate's base tools, from noble. This names explicitly what the old
# golang:bookworm base supplied implicitly: the cgo toolchain (gcc, g++,
# libc6-dev, pkg-config, make), git with openssh-client, curl, wget, gnupg,
# netbase, tzdata, procps and openssl. (It also had mercurial and subversion,
# which only a direct, proxy-less fetch of an hg or svn module needs; the
# gates fetch through GOPROXY.)
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
       bash ca-certificates curl file g++ gcc git gnupg jq libc6-dev make \
       netbase openssh-client openssl pkg-config procps python3 python3-pip \
       python3-venv ripgrep rsync shellcheck tzdata unzip wget xauth xvfb \
       xz-utils zip \
  && rm -rf /var/lib/apt/lists/* \
  && gcc --version | head -1 && git --version

# go from go.dev's official tarball, checked against its published sha256.
# GateEnv sets PATH to the system dirs only, so go and gofmt must be on it.
RUN set -eux; tmp=$(mktemp -d); cd "$tmp"; \
  curl -fsSL -o go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"; \
  echo "${GO_SHA256}  go.tar.gz" | sha256sum -c -; \
  tar -C /usr/local -xzf go.tar.gz; \
  cd /; rm -rf "$tmp"; \
  ln -s /usr/local/go/bin/go /usr/local/bin/go; \
  ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt; \
  go version

# node and npm from the official image; pnpm at the repos' pin. pnpm is
# installed with npm rather than corepack because corepack would download it
# into a writable home at run time.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
  && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
  && npm install -g --no-fund --no-audit "pnpm@${PNPM_VERSION}" \
  && npm cache clean --force \
  && node --version && pnpm --version

# task, buf, kcl and yq from their releases, each checked against the release's
# own checksums file.
RUN set -eux; tmp=$(mktemp -d); cd "$tmp"; \
  base=https://github.com/go-task/task/releases/download/v${TASK_VERSION}; \
  curl -fsSLO "$base/task_linux_amd64.tar.gz"; curl -fsSLO "$base/task_checksums.txt"; \
  grep ' task_linux_amd64.tar.gz$' task_checksums.txt | sha256sum -c -; \
  tar -xzf task_linux_amd64.tar.gz task; install -m 0755 task /usr/local/bin/task; \
  base=https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}; \
  curl -fsSLO "$base/buf-Linux-x86_64"; curl -fsSLO "$base/sha256.txt"; \
  grep ' buf-Linux-x86_64$' sha256.txt | sha256sum -c -; \
  install -m 0755 buf-Linux-x86_64 /usr/local/bin/buf; \
  base=https://github.com/kcl-lang/cli/releases/download/v${KCL_VERSION}; \
  tgz=kcl-v${KCL_VERSION}-linux-amd64.tar.gz; \
  curl -fsSLO "$base/$tgz"; curl -fsSLO "$base/cli_${KCL_VERSION}_checksums.txt"; \
  grep " $tgz\$" "cli_${KCL_VERSION}_checksums.txt" | sha256sum -c -; \
  tar -xzf "$tgz" kcl; install -m 0755 kcl /usr/local/bin/kcl; \
  base=https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}; \
  curl -fsSL -o yq "$base/yq_linux_amd64"; \
  install -m 0755 yq /usr/local/bin/yq; \
  cd /; rm -rf "$tmp"; \
  task --version; buf --version; kcl --version; yq --version

COPY --from=gotools /out/golangci-lint /usr/local/bin/golangci-lint
COPY --from=forgebuild /out/forge /usr/local/bin/forge
RUN golangci-lint --version && forge --version

# Playwright's browsers, installed read-only under /opt along with their system
# packages. The gate runs `npx playwright install`, which writes a lock and
# links into its browsers dir, so that dir (PLAYWRIGHT_BROWSERS_PATH, below)
# is under the writable /gate/cache and holds symlinks to these. A later
# Playwright that wants other builds downloads them there.
RUN PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
     npx -y "playwright@${PLAYWRIGHT_VERSION}" install --with-deps chromium webkit \
  && rm -rf /var/lib/apt/lists/* /root/.npm \
  && chmod -R a+rX /opt/ms-playwright

# The non-root gate user and the writable layout under /gate. The uid is not
# 1000, so the gate never shares a uid with the machine's first login user
# (ubuntu:24.04 ships an `ubuntu` user at 1000, which stays unused here).
# /gate/trusted-go-build is an empty mount point; the runc backend mounts the
# machine's trusted tier over it, read-only.
RUN useradd --uid "${GATE_UID}" --user-group --home-dir /gate/home --no-create-home --shell /bin/bash gate \
  && mkdir -p /gate/src /gate/out /gate/artifacts /gate/home /gate/tmp \
       /gate/cache/go-build /gate/cache/go-mod /gate/cache/gopath \
       /gate/cache/pnpm-store /gate/cache/npm /gate/cache/corepack /gate/cache/xdg \
       /gate/cache/ms-playwright /gate/trusted-go-build \
  && for d in /opt/ms-playwright/*/; do ln -s "$d" "/gate/cache/ms-playwright/$(basename "$d")"; done \
  && chown -R gate:gate /gate

# GOTOOLCHAIN=auto: if a repo's go.mod moves its toolchain past this image's
# go, the gate downloads the new one (checked against the checksum database)
# into GOMODCACHE, and does not fail. GOFLAGS and GOCACHEPROG: see "One Go
# build cache per machine" above.
ENV HOME=/gate/home \
    GOTOOLCHAIN=auto \
    GOFLAGS=-trimpath \
    GOCACHE=/gate/cache/go-build \
    GOCACHEPROG="/usr/local/bin/forge cache prog --trusted /gate/trusted-go-build" \
    GOMODCACHE=/gate/cache/go-mod \
    GOPATH=/gate/cache/gopath \
    XDG_CACHE_HOME=/gate/cache/xdg \
    XDG_CONFIG_HOME=/gate/home/.config \
    XDG_DATA_HOME=/gate/home/.local/share \
    XDG_STATE_HOME=/gate/home/.local/state \
    npm_config_cache=/gate/cache/npm \
    npm_config_store_dir=/gate/cache/pnpm-store \
    npm_config_update_notifier=false \
    COREPACK_HOME=/gate/cache/corepack \
    PLAYWRIGHT_BROWSERS_PATH=/gate/cache/ms-playwright \
    RELIANT_GATE_CACHE=/gate/cache

USER gate
WORKDIR /gate/src
