#!/usr/bin/env bash
# integration-packages.sh — the Go packages that HAVE integration-tagged test
# files (`//go:build ... integration ...`), one `./dir` per line. Nothing when
# there are none.
#
# `task test:integration` tests exactly these. It used to test `./...` with
# -tags=integration, and reliant has no integration-tagged file at all
# (2026-10-30), so the release gate's integration step re-ran the entire
# -race unit suite a second time: 591-897s per run, for no test the unit
# step had not just run.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
git ls-files -z -- '*_test.go' \
  | xargs -0 -r grep -l -E '^//go:build .*\bintegration\b' 2>/dev/null \
  | xargs -r -n1 dirname \
  | sort -u \
  | sed 's|^|./|' \
  || true
