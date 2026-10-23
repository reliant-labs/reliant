#!/usr/bin/env bash
# Refresh .github/e2e-timings.json — the recorded durations the e2e shards are
# planned from (internal/e2eshard; .github/workflows/e2e-suite.yml) — from CI.
#
#   scripts/e2e-timings.sh              the latest completed Nightly run on main
#                                       (it runs every e2e test), else the
#                                       latest E2E Suite run on main
#   scripts/e2e-timings.sh <run-id>...  those runs; a test's median across them
#                                       is what gets recorded
#
# Tests the runs did not execute keep their recorded duration, and entries for
# tests that no longer exist are dropped. Commit the result. Nothing breaks
# when the file is stale: a test with no entry is planned at the median and
# named in the plan's summary, so this is upkeep for balance, not correctness.
#
# Needs `gh` authenticated for reliant-labs/forge, and go.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
repo=reliant-labs/forge
timings=.github/e2e-timings.json

latest_run() {
  gh run list -R "$repo" --workflow "$1" --branch main --status completed --limit 20 \
    --json databaseId,conclusion -q '[.[] | select(.conclusion != "cancelled")][0].databaseId // empty'
}

runs=("$@")
if [ ${#runs[@]} -eq 0 ]; then
  run=$(latest_run nightly.yml)
  [ -n "$run" ] || run=$(latest_run e2e-suite.yml)
  [ -n "$run" ] || { echo "no completed Nightly or E2E Suite run on main to read" >&2; exit 1; }
  runs=("$run")
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for r in "${runs[@]}"; do
  echo "downloading shard results of run $r"
  gh run download "$r" -R "$repo" -p 'e2e-shard-*' -D "$tmp/$r" >/dev/null
done

shopt -s nullglob
files=("$tmp"/*/e2e-shard-*/e2e-shard-*.json)
if [ ${#files[@]} -eq 0 ]; then
  echo "run(s) ${runs[*]} hold no e2e shard results (artifacts expire after 7 days)" >&2
  exit 1
fi

go run ./internal/e2eshard timings -merge "$timings" -dir internal/cli "${files[@]}" > "$tmp/timings.json"
mv "$tmp/timings.json" "$timings"
git --no-pager diff --stat -- "$timings"
