#!/usr/bin/env bash
# Run one shard of main's full test lanes: -race, full mode (no -short).
#
#   scripts/ci-test-full-shard.sh unit I N   package shard I of N of the unit set
#   scripts/ci-test-full-shard.sh cli I N    test shard I of N of internal/cli
#
# ci.yml's `test` and `test-cli` matrices call this once per shard. Before the
# split each lane was one job and the release waited on the slower of them:
# `go test -race` of the unit set took 15-17 minutes on one 4-core runner, and
# internal/cli alone 1177s (2026-10-30, main 29144a24), so a release waited
# ~25 minutes for main's full tests. Split, each shard is a runner of its own.
#
# HOW EACH LANE SPLITS.
#   unit  by package (scripts/ci-test-packages.sh unit-shard), balanced on the
#         recorded package times in .github/full-test-timings.json.
#   cli   internal/cli is ONE package, so it splits by top-level test: the race
#         test binary lists its tests (`go test -list`, which builds the binary
#         the run then reuses from the build cache), they are dealt round-robin
#         in name order, and the shard runs its names with an anchored -run.
#         Every top-level test lands in exactly one shard; subtests follow
#         their parent. TestMain runs in every shard, as it does in any run.
#
# A shard with nothing to run fails: it would pass vacuously.
#
# OUTPUT. The `go test -json` stream goes to $CI_TEST_JSON (default
# test-full.json); the console gets package lines, build errors and the output
# of every failed test, and $GITHUB_STEP_SUMMARY the slowest tests, which is
# the data to rebalance the shards from.
set -euo pipefail

json=${CI_TEST_JSON:-test-full.json}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
# Per test BINARY: a hang detector, as in the unsplit jobs.
timeout=${CI_TEST_TIMEOUT:-30m}

if [[ $# -ne 3 ]]; then
  echo "usage: $0 unit|cli I N" >&2
  exit 2
fi
lane=$1 i=$2 n=$3
if ! [[ "$i" =~ ^[0-9]+$ && "$n" =~ ^[1-9][0-9]*$ ]] || ((i >= n)); then
  echo "usage: $0 unit|cli I N   (0 <= I < N)" >&2
  exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)

args=()
case "$lane" in
  unit)
    pkgs=()
    while IFS= read -r p; do pkgs+=("$p"); done < <("$here/ci-test-packages.sh" unit-shard "$i" "$n")
    if [[ ${#pkgs[@]} -eq 0 ]]; then
      echo "::error::unit shard $i of $n is empty; lower the shard count"
      exit 1
    fi
    echo "::group::unit shard $((i + 1)) of $n: ${#pkgs[@]} packages"
    printf '%s\n' "${pkgs[@]}"
    echo "::endgroup::"
    args=("${pkgs[@]}")
    what="${#pkgs[@]} packages"
    ;;
  cli)
    pkg=$("$here/ci-test-packages.sh" cli)
    listed=$("$here/ci-go-test.sh" -race -list '.*' "$pkg") || {
      printf '%s\n' "$listed"
      echo "::error::cannot list $pkg's tests (does it compile?)"
      exit 1
    }
    names=()
    while IFS= read -r t; do names+=("$t"); done < <(printf '%s\n' "$listed" | grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' | sort | awk -v i="$i" -v n="$n" '(NR - 1) % n == i')
    if [[ ${#names[@]} -eq 0 ]]; then
      echo "::error::internal/cli shard $i of $n is empty; lower the shard count"
      exit 1
    fi
    echo "::group::internal/cli shard $((i + 1)) of $n: ${#names[@]} top-level tests"
    printf '%s\n' "${names[@]}"
    echo "::endgroup::"
    regex="^($(IFS='|'; echo "${names[*]}"))\$"
    # Linux caps ONE argument at 128 KiB (MAX_ARG_STRLEN), and the regex
    # reaches the test binary as one -test.run argument. A third of
    # internal/cli's 4,033 tests made 57 KB (2026-10-30). Fail naming the fix
    # rather than with E2BIG.
    if ((${#regex} > 120000)); then
      echo "::error::internal/cli shard $i of $n names ${#names[@]} tests (${#regex}-byte -run regex, over Linux's 128 KiB argument cap); raise test-cli's shard count in ci.yml"
      exit 1
    fi
    args=(-run "$regex" "$pkg")
    what="${#names[@]} internal/cli tests"
    ;;
  *)
    echo "usage: $0 unit|cli I N" >&2
    exit 2
    ;;
esac

status=0
started=$SECONDS
"$here/ci-go-test.sh" -race -count=1 -json -timeout "$timeout" "${args[@]}" >"$json" || status=$?
elapsed=$((SECONDS - started))
echo "$lane shard $((i + 1))/$n: ${elapsed}s wall (compile, link and tests)"

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
  echo "### $lane $((i + 1))/$n: $what"
  echo
  echo "Command wall time: ${elapsed}s (compile/link/tests)."
  echo
  echo "| passed | failed | skipped |"
  echo "| --- | --- | --- |"
  echo "| $(count pass) | $(count fail) | $(count skip) |"
  echo
  echo "<details><summary>slowest packages and tests</summary>"
  echo
  echo "| seconds | package | test |"
  echo "| ---: | --- | --- |"
  # awk, not head: head exits early, and under pipefail the SIGPIPE it hands
  # sort would fail the step.
  jq -r 'select((.Action == "pass" or .Action == "fail") and (.Test == null or (.Test | contains("/") | not))) | [.Elapsed, (.Package | sub("^github.com/reliant-labs/forge/"; "")), (.Test // "(package)")] | @tsv' "$json" |
    sort -rn | awk -F'\t' 'NR <= 20 {printf "| %.2f | %s | `%s` |\n", $1, $2, $3}'
  echo
  echo "</details>"
  echo
} >>"$summary"

exit $status
