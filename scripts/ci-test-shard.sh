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
#
# RECORD. forge dogfoods its own test record: the run goes through
# `forge test capture --lane unit` (this checkout's forge, scripts/ci-forge.sh)
# into .forge/tests/runs/<run>/unit/ — the -json stream as results.json and,
# with -covermode=atomic -coverprofile, the lane's coverage as cover.out.
# Capture changes nothing about the run: it execs the same command, prints
# the stream unchanged (--render raw, so $CI_TEST_JSON is what it was) and
# exits with the command's status. Replay survives it: -coverprofile is a
# cacheable flag whose path Go's cache ignores, the cover build is main's
# warmed variant too (test-short-warm runs this script), and
# scripts/ci-go-test.sh drops the FORGE_TEST_* variables capture exports
# before cmd/go starts. FORGE_TESTS=off records nothing.
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

# The run to record into ($FORGE_TEST_RUN joins one an outer lane began), and
# its lane directory, which -coverprofile writes into: made here, so a run
# recorded nowhere (FORGE_TESTS=off) still has a place for the profile.
# Before the clock: building forge is setup, not test time.
run=$(scripts/ci-forge.sh test begin)
mkdir -p "$run/unit"

# QUARANTINE. forge.yaml's tests.flakes.quarantine: pr opens a pull request
# that writes a ci/quarantine/ entry for a confirmed flake; the lanes skip
# what the list names (`forge test quarantine skip-regex go`, empty when it
# names nothing, so the command is unchanged then, and test-short-warm, which
# runs this script on the same tree, skips the same tests). Entries expire
# after 48h and stay applied, reported overdue, until the test is deleted or
# rewritten (owner: flaky = delete). The flake gate does not skip them: a
# change that edits a quarantined test is fixing it.
skip=()
if q=$(scripts/ci-forge.sh test quarantine skip-regex go) && [[ -n "$q" ]]; then
  skip=(-skip "$q")
  echo "skipping quarantined tests: $q"
fi

status=0
started=$SECONDS
scripts/ci-forge.sh test capture --run "$run" --lane unit --render raw -- \
  scripts/ci-go-test.sh -short -json -covermode=atomic -coverprofile="$run/unit/cover.out" -timeout "$timeout" ${skip[@]+"${skip[@]}"} "${pkgs[@]}" >"$json" || status=$?
elapsed=$((SECONDS - started))
echo "shard $i/$n: ${elapsed}s wall (compile, link and tests; includes any cache replay)"
# Observe the test-command budget, leaving setup/artifact headroom within 5m.
# This is deliberately not a shorter per-binary timeout: a timeout hides the
# slow test's result instead of explaining the critical path.
if ((elapsed > ${CI_TEST_BUDGET_SECONDS:-240})); then
  echo "::warning::test shard exceeded its ${CI_TEST_BUDGET_SECONDS:-240}s command budget (${elapsed}s); inspect slow tests and compile/cache time"
fi

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

# A PASSING test may raise a warning by logging a titled workflow command
# (`::warning title=<what>::<message>`, e.g. the skill-headroom guard in
# internal/templates). Its output is otherwise dropped with the rest of a
# passing test's, so echo each such line to make it an annotation.
jq -r 'select(.Action == "output" and .Test != null) | .Output | select(contains("::warning title=")) | .[index("::warning title="):] | rtrimstr("\n")' "$json" | tr -d '\r' | sort -u

top='select((.Action == "pass" or .Action == "fail" or .Action == "skip") and .Test != null and (.Test | contains("/") | not))'
count() { jq -r "$top | select(.Action == \"$1\") | .Test" "$json" | grep -c . || true; }
{
  echo "### packages $((i + 1))/$n — ${#pkgs[@]} packages"
  echo
  echo "Command wall time: ${elapsed}s (compile/link/tests, not just test bodies). Budget: ${CI_TEST_BUDGET_SECONDS:-240}s."
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
