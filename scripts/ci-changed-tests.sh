#!/usr/bin/env bash
# The flake gate: run every test a change ADDS or EDITS three times.
# Premerge reuses the short tier's non-race artifacts; main/full adds -race.
#
#   scripts/ci-changed-tests.sh [BASE] [short|full]   defaults: HEAD^1 short
#
# forge dogfoods its own flake gate: this is a thin wrapper over
# `forge test flakes gate` (this checkout's forge, scripts/ci-forge.sh), the
# port of what this script did, kept so ci.yml and scripts/ci-premerge.sh
# call it unchanged. `forge test flakes gate --help` says how it finds the
# tests (a `+func TestX(` line, or a hunk inside `func TestX(`; e2e- and
# integration-tagged files and files outside the module's packages skipped)
# and runs them: one `go test -json -short -count=3 -timeout 10m [-race]
# -run '^(…)$' <packages>` through scripts/ci-go-test.sh. The run is
# recorded as lane flake-gate under .forge/tests/runs/, so a retry pass feeds
# `forge test flakes`.
#
# On a pull request, actions/checkout checks out the merge commit
# (refs/pull/N/merge), whose first parent is the base branch's tip — so with
# fetch-depth: 2, `HEAD^1..HEAD` is exactly what the pull request changes. In
# the merge queue HEAD is the entry's squash commit and HEAD^1 is main plus
# preceding entries, so the range again names this change alone.
#
# WHY. "Passed on re-run" is never an acceptable verdict here, and the cheapest
# moment to catch a flaky test is before it lands: once merged, an
# intermittent failure surfaces weeks later in somebody else's pull request,
# looking like their bug. A test run once proves it CAN pass; three runs are a
# cheap probe that it passes reliably — order dependence, leaked global state,
# a timing assumption. The race detector joins them on main (`full`).
#
# WHY -count=3 IS NOT A CACHE LOSS. The pre-merge tier never runs -count=1
# (the owner's rule: reuse Go's test cache). -count defeats the cache too, but
# only the tests selected here run under it — tests the change adds or edits,
# whose package's test binary changed, so no cached result existed for them.
# Every other test in the package is excluded by -run, not re-run.
#
# EXIT. 0 every changed test passed every run, 1 one failed, or the test
# command could not start. The gate exits 2 when it identifies no changed
# test, and this script always exited 0 for a change with no new or edited
# tests, so 2 is 0 here. That includes a diff the gate cannot read (a base
# that is not a commit): the script this wraps passed that too, because its
# git diff ran in a process substitution `set -e` does not see. The verdict
# is kept, and that case is a ::warning:: instead of a silent pass.
set -euo pipefail

base=${1:-HEAD^1}
mode=${2:-short}
args=(--base "$base")
case "$mode" in
  short) ;;
  full) args+=(--full) ;;
  *) echo "usage: $0 [BASE] [short|full]" >&2; exit 2 ;;
esac
[[ $# -le 2 ]] || { echo "usage: $0 [BASE] [short|full]" >&2; exit 2; }

log=$(mktemp)
trap 'rm -f "$log"' EXIT
# The gate's stderr goes to the terminal and into $log through a PIPELINE, not
# a process substitution: bash returns from a pipeline only once tee has
# exited, so $log is complete before it is read. A bare `wait` does not wait
# for a `2> >(tee …)` (bash 5.2), and reading $log while tee was still
# writing it turned "could not run" into a pass now and then (forge#2714).
# fd 3 carries the gate's stdout past the pipe.
exec 3>&1
set +e
scripts/ci-forge.sh test flakes gate "${args[@]}" -- scripts/ci-go-test.sh 2>&1 1>&3 3>&- | tee "$log" >&2 3>&-
status=${PIPESTATUS[0]}
set -e
exec 3>&-
if [[ $status -eq 2 ]]; then
  if grep -q '^flake gate: could not run' "$log"; then
    status=1 # the test command did not start: a failure, as it always was
  elif grep -q '^flake gate: could not identify' "$log"; then
    echo "::warning::$(grep -m1 '^flake gate: could not identify' "$log"); passing, as this gate always has"
    status=0
  else
    status=0 # no new or edited tests
  fi
fi
exit "$status"
