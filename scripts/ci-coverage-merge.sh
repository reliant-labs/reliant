#!/usr/bin/env bash
# Merge main's full-mode Go coverage profiles into one, for Codecov.
#
#   scripts/ci-coverage-merge.sh [<runs-dir>] [<out>]
#
# coverage.yml runs it when a CI run of a push to main completes, after it
# downloads that run's test-runs-* artifacts into <runs-dir> (default
# .forge/tests/runs). Each recording lane wrote its profile to
# <runs-dir>/<run>/<lane>/cover.out (scripts/ci-test-full-shard.sh,
# ci-test-shard.sh, ci.yml's integration steps). It writes <out> (default
# coverage.out), one profile Codecov reads in a single upload.
#
# WHICH LANES. The full-mode ones: `*-race` (the unit and internal/cli shards,
# -race, no -short) and `integration*` (the -tags=integration steps). The
# -short tier (`unit`) and the flake gate are subsets of them and are left out,
# so the report is main's full suite and nothing else.
#
# HOW. A Go profile is a `mode:` line, then one line per block. The full lanes
# run -covermode=atomic; the integration steps that pass no -covermode run in
# set mode, whose 0/1 counts are valid atomic counts too. So the merge is one
# `mode: atomic` line and every block line of every profile. A block that
# several lanes ran appears once per lane; Codecov merges those lines by
# position, as `go tool cover` does across packages.
#
# Generated code is excluded by codecov.yml's `ignore`, not here: the report
# keeps every file, and Codecov's view drops what the list names
# (internal/ciguard holds the list to every generated file in the tree).
#
# Exits 1 when no full-lane profile exists: an empty report is not a 0%
# report, and the upload step must not send one.
set -euo pipefail

runs=${1:-.forge/tests/runs}
out=${2:-coverage.out}

profiles=()
shopt -s nullglob
for p in "$runs"/*/*-race/cover.out "$runs"/*/integration*/cover.out; do
  [[ -s "$p" ]] && profiles+=("$p")
done
shopt -u nullglob

if [[ ${#profiles[@]} -eq 0 ]]; then
  echo "ci-coverage-merge: no full-lane cover.out under $runs (*-race, integration*)" >&2
  exit 1
fi

{
  echo "mode: atomic"
  # awk, not tail -q: drop each file's own mode line wherever it is, and
  # skip blank lines a truncated profile could end with.
  awk 'FNR == 1 && /^mode:/ { next } NF { print }' "${profiles[@]}"
} >"$out"

echo "ci-coverage-merge: ${#profiles[@]} profiles, $(($(wc -l <"$out") - 1)) blocks -> $out"
printf '  %s\n' "${profiles[@]}"
