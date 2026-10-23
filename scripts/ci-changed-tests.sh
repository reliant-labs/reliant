#!/usr/bin/env bash
# The flake gate: run every test a change ADDS or EDITS three times before it
# merges.
#
#   scripts/ci-changed-tests.sh [BASE]      BASE defaults to HEAD^1
#
# On a pull request, actions/checkout checks out the merge commit
# (refs/pull/N/merge), whose first parent is the base branch's tip — so with
# fetch-depth: 2, `HEAD^1..HEAD` is exactly what the pull request changes. In
# the merge queue (merge_group) HEAD is the entry's squash commit and HEAD^1 is
# main plus the entries ahead of it, so the range is again this change alone.
#
# WHY. "Passed on re-run" is never an acceptable verdict here, and the cheapest
# moment to catch a flaky test is before it lands: once merged, an
# intermittent failure surfaces weeks later in somebody else's pull request,
# looking like their bug. A test run once proves it CAN pass; three runs are a
# cheap probe that it passes reliably — order dependence, leaked global state,
# a timing assumption.
#
# NO -race. It runs in ci.yml's test-short job, right after the -short tier,
# so the three runs reuse the test binaries that tier just compiled. -race is
# a separate build of the whole dependency graph, and restoring it cost the
# old flake-gate job 55-74s of a pull request's five-minute budget, plus a
# runner of its own. The race detector runs over every test on each push to
# main (ci.yml `test`, `test-cli`, full mode), and a race it finds is
# bisected from there.
#
# WHICH TESTS. Top-level Test/Fuzz functions in *_test.go files the change
# adds or modifies, found two ways in the diff:
#   - a `+func TestX(` line: the test is new, or its signature line moved;
#   - a hunk whose header names `func TestX(`: git's default funcname rule
#     labels each hunk with the nearest preceding unindented line, which in Go
#     is the enclosing top-level declaration — so an edited test body is found.
# An edit to a shared helper names no test, and is not chased: this is a cheap
# gate on new and edited tests, not a substitute for the full tier on main.
#
# Files built only under the e2e or integration tag are skipped: they need the
# tag and their provisioned tools (e2e-suite.yml, `task test:integration`).
# A name that a build constraint keeps out of this platform simply matches
# nothing.
set -euo pipefail

base=${1:-HEAD^1}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}

# The module's own package directories, relative: a test file under testdata/,
# or in a nested module (examples/, a fixture with its own go.mod), is not a
# package `go test ./<dir>` can build from here.
root=$(pwd -P)
declare -A in_module=()
while IFS= read -r d; do in_module[${d#"$root"/}]=1; done < <(go list -f '{{.Dir}}' ./...)

declare -A by_dir=()
while IFS= read -r f; do
  [[ -f "$f" ]] || continue
  if [[ -z "${in_module[$(dirname "$f")]:-}" ]]; then
    echo "skip (not a package of this module): $f"
    continue
  fi
  if grep -qE '^//go:build .*\b(e2e|integration)\b' "$f"; then
    echo "skip (tagged): $f"
    continue
  fi
  names=$(git diff -U0 "$base" HEAD -- "$f" |
    sed -nE 's/^\+func ((Test|Fuzz)[A-Za-z0-9_]*)\(.*/\1/p; s/^@@ .* @@ func ((Test|Fuzz)[A-Za-z0-9_]*)\(.*/\1/p' |
    sort -u)
  [[ -n "$names" ]] || continue
  dir=$(dirname "$f")
  by_dir[$dir]+="$names"$'\n'
done < <(git diff --name-only --diff-filter=AM "$base" HEAD -- '*_test.go')

if [[ ${#by_dir[@]} -eq 0 ]]; then
  echo "no new or edited tests in $base..HEAD"
  echo "flake gate: no new or edited tests in this change." >>"$summary"
  exit 0
fi

status=0
{
  echo "### flake gate: new and edited tests, -count=3"
  echo
} >>"$summary"
for dir in $(printf '%s\n' "${!by_dir[@]}" | sort); do
  names=$(printf '%s' "${by_dir[$dir]}" | grep . | sort -u)
  n=$(grep -c . <<<"$names")
  run="^($(paste -sd'|' - <<<"$names"))$"
  echo "::group::./$dir — $n test(s)"
  echo "$names"
  echo "::endgroup::"
  if go test -short -count=3 -timeout 10m -run "$run" "./$dir"; then
    echo "- \`./$dir\`: $n test(s) passed 3/3" >>"$summary"
  else
    status=1
    echo "- \`./$dir\`: **FAILED** — one of $n test(s) did not pass three times in a row" >>"$summary"
    echo "::error::./$dir: a new or edited test did not pass 3 of 3 runs. Reproduce with: go test -short -count=3 -run '$run' ./$dir"
  fi
done
exit $status
