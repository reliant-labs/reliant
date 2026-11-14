#!/usr/bin/env bash
# scripts/bootstrap.sh — install the tools this repo expects.
#
# Safe to re-run: every step checks whether the tool is already on PATH.
# Intended for devcontainer postCreateCommand and new-contributor setup.
set -euo pipefail

# The KCL CLI release to install. Kept equal to CI's pin and to the
# kcl-lang.io modules in go.mod by TestKCLVersionIsPinnedEverywhere.
KCL_VERSION=v0.13.2

log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

need_go() {
  if ! have go; then
    echo "go is required but not found on PATH." >&2
    echo "Install Go (matching the go.mod directive): https://go.dev/dl/" >&2
    exit 1
  fi
}

install_go_tool() {
  # $1: binary name, $2: go install path
  local bin="$1" pkg="$2"
  if have "$bin"; then
    log "$bin already installed — skipping"
    return
  fi
  log "installing $bin via go install"
  go install "$pkg"
}

install_task() {
  if have task; then
    log "task already installed — skipping"
    return
  fi
  log "installing Task (https://taskfile.dev)"
  # Official install script; respects GOBIN / $HOME/.local/bin.
  sh -c "$(curl --location https://taskfile.dev/install.sh)" -- -d -b "${GOBIN:-$HOME/go/bin}"
}

install_buf() {
  if have buf; then
    log "buf already installed — skipping"
    return
  fi
  log "installing buf (https://buf.build)"
  install_go_tool buf github.com/bufbuild/buf/cmd/buf@latest
}

install_kcl() {
  if have kcl; then
    log "kcl already installed — skipping"
    return
  fi
  log "installing KCL ${KCL_VERSION} (https://kcl-lang.io)"
  # KCL ships its own installer; fall back to a printed instruction on
  # platforms where curl isn't available. Pinned, like CI: unpinned, the
  # installer resolves "latest" through an unauthenticated, rate-limited
  # api.github.com call.
  if have curl; then
    curl -fsSL https://kcl-lang.io/script/install-cli.sh | KCL_VERSION="${KCL_VERSION}" bash
  else
    echo "curl missing — install KCL manually: https://kcl-lang.io/docs/user_docs/getting-started/install" >&2
  fi
}

main() {
  need_go

  install_task
  install_buf
  install_kcl

  install_go_tool protoc-gen-go            google.golang.org/protobuf/cmd/protoc-gen-go@latest
  install_go_tool protoc-gen-connect-go    connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
  # v2 module path and an explicit version: `.../golangci-lint/cmd/...@latest`
  # is the v1 module (v1.64.8), which cannot read a `version: "2"` config.
  # Kept equal to CI's pin by TestGolangciLintVersionIsPinnedEverywhere.
  install_go_tool golangci-lint            github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
  install_go_tool goimports                golang.org/x/tools/cmd/goimports@latest
  install_go_tool govulncheck              golang.org/x/vuln/cmd/govulncheck@latest

  log "bootstrap complete"
}

main "$@"
