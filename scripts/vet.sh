#!/usr/bin/env bash
# The existing vet lane: Go's standard analyzers, then the app-owned typed
# model rule. go vet supplies complete per-package type information (including
# tests), rejects type errors and caches results. No nested packages.Load.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -eq 0 ]; then set -- ./...; fi

go vet "$@"
# Build the driver only; the analyzed packages are compiled by vet, not by the
# race-instrumented test process. A private temp path avoids shared writers.
tmp=$(mktemp -d "${TMPDIR:-/tmp}/reliant-modelvet.XXXXXXXX")
trap 'rm -rf "$tmp"' EXIT
go build -o "$tmp/modelvet" ./tools/modelvet
go vet -vettool="$tmp/modelvet" "$@"
