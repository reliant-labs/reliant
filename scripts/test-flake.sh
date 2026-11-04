#!/usr/bin/env bash
# test-flake.sh — the new-flake check: every Go Test function the change adds
# or edits runs three more times.
#
#   scripts/test-flake.sh [--base <rev>]   (default: the merge-base with origin/main)
#
# A test that passes once and fails on a repeat is a flake, and "passed on
# re-run" is never a reason to merge. The cheapest moment to catch one is the
# change that wrote it, so `tools/test-affected changed-tests` names the Test
# functions this diff adds or edits (by function, not by file) and each runs
# `-short -count=3`. A test that leans on leftover state, a fixed port,
# wall-clock timing or test order fails here instead of on main a week later.
#
# `task test:flake` runs it; CI runs it beside the merge gate on every PR.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2

base=""
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base=${2:?--base needs a rev}; shift 2 ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) echo "test-flake: unknown argument $1" >&2; exit 2 ;;
  esac
done
[ -n "$base" ] || base=$(git merge-base HEAD origin/main) || { echo "test-flake: no merge-base with origin/main (fetch it, or pass --base)" >&2; exit 2; }

targets=$(go run ./tools/test-affected changed-tests -base "$base") || { echo "test-flake: tools/test-affected changed-tests failed" >&2; exit 2; }
if [ -z "$targets" ]; then
  echo "test-flake: no new or changed Go tests"
  exit 0
fi

annotate=false; [ -n "${GITHUB_ACTIONS:-}" ] && annotate=true
status=0
while IFS=$'\t' read -r pkg pattern; do
  [ -n "$pkg" ] || continue
  $annotate && echo "::group::$pkg -run '$pattern' -count=3"
  echo "test-flake: $pkg -run '$pattern' -count=3"
  if ! go test -short -count=3 -timeout=10m -run "$pattern" "$pkg"; then
    msg="$pkg $pattern did not pass three runs in a row. A test that fails on a repeat is a flake; fix it at the root (shared state, a fixed port, wall-clock timing, test order)."
    if $annotate; then echo "::error title=Flaky or failing new test::$msg"; else echo "test-flake: FAIL: $msg" >&2; fi
    status=1
  fi
  $annotate && echo "::endgroup::"
done <<< "$targets"
exit "$status"
