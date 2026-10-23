#!/usr/bin/env bash
# Run one shard of the pull-request test tier: -short, no -race.
#
#   scripts/ci-test-shard.sh I N      package shard I of N (0-based)
#
# ci.yml's `test-short` matrix calls this once per shard. The partition lives
# in scripts/ci-test-packages.sh (`short I N`); this script runs a shard and
# reports it.
#
# `go test -short` over the shard's packages: the packages run side by side
# (-p = the runner's cores), vet runs, and a package whose inputs main's build
# cache already holds a result for replays it. The -short tier is held
# cache-sound (reliant.md, "Testing tiers"), so that replay is correct.
#
# OUTPUT. The `go test -json` stream goes to $CI_TEST_JSON (default
# test-shard.json) so it can be uploaded; the console gets the package lines
# (ok/FAIL), build errors, and the full output of every failed test — the
# picture plain `go test` prints, without the -v noise -json implies.
# $GITHUB_STEP_SUMMARY gets the counts and the shard's slowest tests, which is
# where a test that crept past the ~2s -short budget shows up first.
set -euo pipefail

json=${CI_TEST_JSON:-test-shard.json}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
# Per test BINARY. A hang detector, not a speed bound: the job's own
# timeout-minutes is the budget.
timeout=${CI_TEST_TIMEOUT:-10m}

if [[ $# -ne 2 ]]; then
  echo "usage: $0 I N" >&2
  exit 2
fi
i=$1 n=$2

pkgs=()
while IFS= read -r p; do pkgs+=("$p"); done < <(scripts/ci-test-packages.sh short "$i" "$n")
if [[ ${#pkgs[@]} -eq 0 ]]; then
  echo "::error::package shard $i of $n is empty — a shard with nothing to run passes vacuously; lower the shard count"
  exit 1
fi
echo "::group::package shard $i of $n — ${#pkgs[@]} packages"
printf '%s\n' "${pkgs[@]}"
echo "::endgroup::"

status=0
go test -short -json -timeout "$timeout" "${pkgs[@]}" >"$json" || status=$?

# Build failures (Go 1.24+ reports them as build-output), then package-level
# lines: ok/FAIL/panics outside any test.
jq -j 'select(.Action == "build-output" or (.Action == "output" and .Test == null)) | .Output' "$json"

failed=$(jq -c 'select(.Action == "fail" and .Test != null) | [.Package, .Test]' "$json" | jq -sc 'unique')
if [[ "$failed" != "[]" ]]; then
  echo
  echo "::group::output of failed tests"
  jq -j --argjson failed "$failed" \
    'select(.Action == "output" and .Test != null) | select([.Package, .Test] as $k | $failed | index([$k])) | .Output' "$json"
  echo "::endgroup::"
  jq -r '.[] | "::error::FAIL \(.[0] | sub("^github.com/reliant-labs/forge/"; "")) \(.[1])"' <<<"$failed"
fi

top='select((.Action == "pass" or .Action == "fail" or .Action == "skip") and .Test != null and (.Test | contains("/") | not))'
count() { jq -r "$top | select(.Action == \"$1\") | .Test" "$json" | grep -c . || true; }
{
  echo "### packages $((i + 1))/$n — ${#pkgs[@]} packages"
  echo
  echo "| passed | failed | skipped |"
  echo "| --- | --- | --- |"
  echo "| $(count pass) | $(count fail) | $(count skip) |"
  echo
  echo "<details><summary>slowest tests</summary>"
  echo
  echo "| seconds | package | test |"
  echo "| ---: | --- | --- |"
  # awk, not head: head exits early, and under pipefail the SIGPIPE it hands
  # sort would fail the step.
  jq -r "$top | select(.Action != \"skip\") | [.Elapsed, (.Package | sub(\"^github.com/reliant-labs/forge/\"; \"\")), .Test] | @tsv" "$json" |
    sort -rn | awk -F'\t' 'NR <= 15 {printf "| %.2f | %s | `%s` |\n", $1, $2, $3}'
  echo
  echo "</details>"
  echo
} >>"$summary"

exit $status
