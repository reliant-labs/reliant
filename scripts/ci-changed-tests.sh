#!/usr/bin/env bash
# The flake gate: run every test a change ADDS or EDITS three times.
# Premerge reuses the short tier's non-race artifacts; main/full adds -race.
#
#   scripts/ci-changed-tests.sh [BASE] [short|full]   defaults: HEAD^1 short
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
mode=${2:-short}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
flags=(-short -count=3 -timeout 10m)
case "$mode" in
  short) ;;
  full) flags+=(-race) ;;
  *) echo "usage: $0 [BASE] [short|full]" >&2; exit 2 ;;
esac
[[ $# -le 2 ]] || { echo "usage: $0 [BASE] [short|full]" >&2; exit 2; }
started=$SECONDS

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

# One go invocation: Go's own -p scheduler bounds package concurrency and
# shares dependency compilation. A shell loop serialized compile/link/run for
# every changed package. The union of names may also repeat a same-named test
# in another selected package; that is safe extra coverage, never an omission.
packages=()
all_names=""
for dir in $(printf '%s\n' "${!by_dir[@]}" | sort); do
  packages+=("./$dir")
  all_names+="${by_dir[$dir]}"
done
names=$(printf '%s' "$all_names" | grep . | sort -u)
run="^($(paste -sd'|' - <<<"$names"))$"
{
  echo "### flake gate ($mode): new and edited tests, ${flags[*]}"
  echo
  # shellcheck disable=SC2016 # Markdown code ticks, not command substitution.
  printf -- '- selected package: `%s`\n' "${packages[@]}"
} >>"$summary"
echo "::group::changed tests ($mode) — ${#packages[@]} packages, scheduled by go test"
echo "$names"
echo "::endgroup::"
status=0
scripts/ci-go-test.sh "${flags[@]}" -run "$run" "${packages[@]}" || status=$?
elapsed=$((SECONDS - started))
if [[ $status -eq 0 ]]; then
  echo "flake gate: selected tests passed 3/3 in ${elapsed}s (${#packages[@]} packages)" | tee -a "$summary"
else
  echo "::error::changed tests failed; reproduce with: go test ${flags[*]} -run '$run' ${packages[*]}"
  echo "flake gate: **FAILED** after ${elapsed}s" >>"$summary"
fi
exit "$status"
