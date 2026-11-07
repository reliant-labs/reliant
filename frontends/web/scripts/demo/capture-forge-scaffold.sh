#!/usr/bin/env bash
# Records the "How this app was built" fixtures for the forge demo
# (src/components/Forge/Demo/howBuilt/data/) from a REAL forge run.
#
# It builds the forge version reliant pins (go.mod's github.com/reliant-labs/forge),
# makes the demo's app (shop: services orders and inventory, a web frontend) in
# a temp dir, writes the two small protos a person would write, and runs
# `forge scaffold`, `forge lint --quiet` and `go build ./...`. The recorder
# (capture-forge-scaffold.mjs) keeps each command's real output and timings,
# then counts every file in the result. Nothing in the fixtures is typed by hand.
#
# Usage (from anywhere; takes a few minutes, most of it `forge lint`):
#   frontends/web/scripts/demo/capture-forge-scaffold.sh
#
# Environment:
#   FORGE_BIN   use this forge binary instead of building one. It must report
#               the pinned version (`forge --version`), or the run stops.
#   KEEP_WORK=1 keep the temp dir (its path is printed) to look at the app.
#
# Re-run it after every forge bump, and commit the two JSON files it rewrites.
# README.md beside this script says what is counted and how.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
web_dir="$(cd "$script_dir/../.." && pwd)"
repo_root="$(cd "$web_dir/../.." && pwd)"
out_dir="$web_dir/src/components/Forge/Demo/howBuilt/data"

version="$(cd "$repo_root" && go list -m -f '{{.Version}}' github.com/reliant-labs/forge)"
if [[ -z "$version" ]]; then
  echo "capture: could not read the pinned forge version from $repo_root/go.mod" >&2
  exit 1
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/forge-demo-capture.XXXXXX")"
cleanup() {
  if [[ "${KEEP_WORK:-}" == "1" ]]; then
    echo "capture: kept $work"
  else
    rm -rf "$work"
  fi
}
trap cleanup EXIT

forge_bin="${FORGE_BIN:-}"
if [[ -z "$forge_bin" ]]; then
  echo "capture: building forge $version"
  mkdir -p "$work/bin"
  GOBIN="$work/bin" GOFLAGS=-mod=mod go install "github.com/reliant-labs/forge/cmd/forge@$version"
  forge_bin="$work/bin/forge"
fi

reported="$("$forge_bin" --version)"
if [[ "$reported" != *"$version"* ]]; then
  echo "capture: $forge_bin reports '$reported', but reliant pins $version" >&2
  exit 1
fi

mkdir -p "$work/home" "$out_dir"
# The recorded commands find `forge` on PATH, as a reader's shell would.
export PATH="$(dirname "$forge_bin"):$PATH"
# A throwaway identity for the temp repo's commits (forge's initial one, and the
# recorder's own bookkeeping commits). It never reaches this repository.
export GIT_AUTHOR_NAME="forge demo capture" GIT_AUTHOR_EMAIL="capture@example.com"
export GIT_COMMITTER_NAME="forge demo capture" GIT_COMMITTER_EMAIL="capture@example.com"

node "$script_dir/capture-forge-scaffold.mjs" \
  --work "$work/home" \
  --out "$out_dir" \
  --forge-version "$version"
