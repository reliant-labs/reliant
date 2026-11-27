#!/usr/bin/env bash
# test-flake.sh — the new-flake check: every Go Test function the change adds
# or edits runs three more times.
#
#   scripts/test-flake.sh [--base <rev>]   (default: the merge-base with origin/main)
#
# A test that passes once and fails on a repeat is a flake, and "passed on
# re-run" is never a reason to merge. The cheapest moment to catch one is the
# change that wrote it: each test this diff adds or edits runs
# `-short -count=3`. A test that leans on leftover state, a fixed port,
# wall-clock timing or test order fails here instead of on main a week later.
#
# reliant dogfoods forge's flake gate: this is a thin wrapper over
# `forge test flakes gate --base <rev> -- scripts/go-test.sh`
# (`forge test flakes gate --help` says how it finds the tests: a `+func
# TestX(` line, or a hunk inside `func TestX(`; e2e- and integration-tagged
# files skipped). It runs them in ONE `go test -json -short -count=3
# -timeout 10m -run '^(…)$' <packages>` and records the run as lane flake-gate
# of forge's test record, so a retry pass feeds `forge test flakes`. The
# tests of a nested Go module (sdk/go) are outside the root module forge's
# gate reads, so they still run here, from their own module
# (tools/test-affected changed-tests names them).
#
# One invocation has one 10-minute budget for every run of every selected
# package. Two ways it judges nothing about flakiness, and both are judged
# again here, one package at a time, from tools/test-affected's per-package
# patterns (each package alone, its changed tests three runs, its own 10m):
#   - the selection is too large for one -run argument (over 8 KiB of names:
#     a wide refactor), so forge runs EVERY test of the selected packages
#     (`-run '^(Test|Fuzz)'`), three times, under that one budget;
#   - the only failures are packages that ran out of the budget (`panic: test
#     timed out`, no `--- FAIL:`, no build failure): load, not a flake
#     (reliant#3723: internal/db, 589 DB tests x3, timed out with
#     TestIsRetryableError still waiting for a t.Parallel slot).
# A changed test that fails alone still fails the gate.
#
# forge.yaml's `lint.rules: {flake-check: error}` is the same policy for
# forge's own gate step; this script blocks on the gate's exit status.
#
# EXIT: 0 every changed test passed three runs (or the change has none), 1 one
# failed or its go test could not start, 2 the changed tests could not be
# identified. `task test:flake` runs it; CI runs it beside the merge gate.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2

base=""
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base=${2:?--base needs a rev}; shift 2 ;;
    -h|--help) sed -n '2,28p' "$0"; exit 0 ;;
    *) echo "test-flake: unknown argument $1" >&2; exit 2 ;;
  esac
done
[ -n "$base" ] || base=$(git merge-base HEAD origin/main) || { echo "test-flake: no merge-base with origin/main (fetch it, or pass --base)" >&2; exit 2; }

annotate=false; [ -n "${GITHUB_ACTIONS:-}" ] && annotate=true
status=0

# repeat <pkg> <pattern> <module>: one nested module's package's changed
# tests, three runs, from that module. Sets status=1 on a failure.
repeat() {
  local pkg=$1 pattern=$2 mod=$3 where rc=0 msg
  where="$mod: $pkg"
  $annotate && echo "::group::$where -run '$pattern' -count=3"
  echo "test-flake: $where -run '$pattern' -count=3"
  (cd "$mod" && GOWORK=off go test -short -count=3 -timeout=10m -run "$pattern" "$pkg") || rc=$?
  if [ "$rc" -ne 0 ]; then
    msg="$where $pattern did not pass three runs in a row. A test that fails on a repeat is a flake; fix it at the root (shared state, a fixed port, wall-clock timing, test order)."
    if $annotate; then echo "::error title=Flaky or failing new test::$msg"; else echo "test-flake: FAIL: $msg" >&2; fi
    status=1
  fi
  $annotate && echo "::endgroup::"
}

# changed_tests: tools/test-affected's `<pkg>\t<pattern>[\t<module>]` lines
# (TEST_FLAKE_CHANGED_CMD <base> replaces it in scripts/test-test-flake.sh).
changed_tests() {
  if [ -n "${TEST_FLAKE_CHANGED_CMD:-}" ]; then "$TEST_FLAKE_CHANGED_CMD" "$base"; return; fi
  go run ./tools/test-affected changed-tests -base "$base" || { echo "test-flake: tools/test-affected changed-tests failed" >&2; return 2; }
}
go_test=${TEST_FLAKE_GO_TEST:-scripts/go-test.sh}

# alone <pkgs>: the root module's changed tests of each of <pkgs> (./dir per
# line, or "all"), one package per go test, three runs, its own 10m budget.
# 0 every one passed; 1 one failed, or there was nothing to run; 2 the changed
# tests could not be identified.
alone() {
  local want=$1 targets pkg pattern mod rc=0 n=0
  targets=$(changed_tests) || return 2
  while IFS=$'\t' read -r pkg pattern mod; do
    [ -n "$pkg" ] && [ -n "$pattern" ] && [ -z "$mod" ] || continue
    [ "$want" = all ] || grep -qxF -- "$pkg" <<<"$want" || continue
    n=$((n + 1))
    echo "test-flake: $pkg alone: its changed tests (${#pattern}-byte -run), -count=3, its own 10m"
    "$go_test" -short -count=3 -timeout 10m -run "$pattern" "$pkg" || rc=1
  done <<<"$targets"
  [ "$n" -gt 0 ] || return 1
  return "$rc"
}

log=$(mktemp)
trap 'rm -f "$log"' EXIT
scripts/forge.sh test flakes gate --base "$base" -- scripts/go-test.sh 2>&1 | tee "$log"
rc=${PIPESTATUS[0]}
# The gate's exit 2 has three meanings, told apart by the line it prints.
# The lines are read whatever the exit status: a wrapper between here and
# forge can turn forge's 2 into a 1 (`go run` did, before scripts/forge.sh
# built the pin instead; reliant#3243's merge-queue regression), and "no new
# or edited tests" is a pass however it arrives.
if [ "$rc" -ne 0 ]; then
  if grep -q '^flake gate: could not run' "$log"; then
    status=1 # the test command did not start: a failure, as it always was
  elif grep -q '^flake gate: could not identify' "$log"; then
    exit 2 # the diff could not be read: as tools/test-affected failing was
  elif grep -q '^flake gate: no new or edited tests' "$log"; then
    : # no new or edited tests in the root module
  else
    [ "$rc" -eq 1 ] || echo "test-flake: forge test flakes gate exited $rc" >&2
    status=1
    # The run judged nothing (every test of the packages, or only out of
    # budget): judge the changed tests again, one package at a time.
    mod=$(awk '$1 == "module" { print $2; exit }' go.mod 2>/dev/null)
    redo=""
    if grep -qF -- "-run '^(Test|Fuzz)'" "$log"; then
      redo=all why="the selection outgrew one -run argument, so the gate ran every test of its packages under one 10m budget"
    elif grep -q '^panic: test timed out' "$log" && ! grep -q -- '--- FAIL: ' "$log" && ! grep -q '\[build failed\]' "$log"; then
      redo=$(sed -nE "s#^FAIL[[:space:]]+${mod//./\\.}/([^[:space:]]+)[[:space:]].*#./\1#p" "$log" | sort -u)
      why="only packages that ran out of the 10m budget failed (load, not a flake)"
    fi
    if [ -n "$redo" ] && [ -n "$mod" ]; then
      echo "test-flake: $why; judging the changed tests one package at a time"
      if alone "$redo"; then
        echo "flake gate: passed one package at a time ($why)"; status=0
      else
        echo "test-flake: FAIL: a changed test failed with its package alone"
      fi
    fi
  fi
fi
# A nested module's tests (sdk/go), from that module.
if git diff --name-only "$base" -- 'sdk/go/*_test.go' | grep -q . || git ls-files --others --exclude-standard -- 'sdk/go/*_test.go' | grep -q .; then
  targets=$(changed_tests) || exit 2
  while IFS=$'\t' read -r pkg pattern mod; do
    [ -n "$pkg" ] && [ -n "$mod" ] || continue
    repeat "$pkg" "$pattern" "$mod"
  done <<<"$targets"
fi
exit "$status"
