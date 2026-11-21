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
# reliant dogfoods forge's flake gate: when the pinned forge has it, this is
# a thin wrapper over `forge test flakes gate --base <rev> -- scripts/go-test.sh`
# (`forge test flakes gate --help` says how it finds the tests: a `+func
# TestX(` line, or a hunk inside `func TestX(`; e2e- and integration-tagged
# files skipped). It runs them in ONE `go test -json -short -count=3
# -timeout 10m -run '^(…)$' <packages>` and records the run as lane flake-gate
# of forge's test record, so a retry pass feeds `forge test flakes`. The
# tests of a nested Go module (sdk/go) are outside the root module forge's
# gate reads, so they still run here, from their own module
# (tools/test-affected changed-tests names them).
#
# A pinned forge without `forge test flakes` (before v1.10.0) runs the check
# this script always ran: tools/test-affected changed-tests names the Test
# functions (by function, not by file), and each package runs
# `go test -short -count=3 -timeout=10m -run <them>`.
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
    -h|--help) sed -n '2,31p' "$0"; exit 0 ;;
    *) echo "test-flake: unknown argument $1" >&2; exit 2 ;;
  esac
done
[ -n "$base" ] || base=$(git merge-base HEAD origin/main) || { echo "test-flake: no merge-base with origin/main (fetch it, or pass --base)" >&2; exit 2; }

annotate=false; [ -n "${GITHUB_ACTIONS:-}" ] && annotate=true
status=0

# repeat <pkg> <pattern> <module>: one package's changed tests, three runs,
# from its own module when it is a nested one. Sets status=1 on a failure.
repeat() {
  local pkg=$1 pattern=$2 mod=$3 where rc=0 msg
  where=$pkg; [ -n "$mod" ] && where="$mod: $pkg"
  $annotate && echo "::group::$where -run '$pattern' -count=3"
  echo "test-flake: $where -run '$pattern' -count=3"
  if [ -n "$mod" ]; then
    (cd "$mod" && GOWORK=off go test -short -count=3 -timeout=10m -run "$pattern" "$pkg") || rc=$?
  else
    go test -short -count=3 -timeout=10m -run "$pattern" "$pkg" || rc=$?
  fi
  if [ "$rc" -ne 0 ]; then
    msg="$where $pattern did not pass three runs in a row. A test that fails on a repeat is a flake; fix it at the root (shared state, a fixed port, wall-clock timing, test order)."
    if $annotate; then echo "::error title=Flaky or failing new test::$msg"; else echo "test-flake: FAIL: $msg" >&2; fi
    status=1
  fi
  $annotate && echo "::endgroup::"
}

# changed_tests: tools/test-affected's `<pkg>\t<pattern>[\t<module>]` lines.
changed_tests() {
  go run ./tools/test-affected changed-tests -base "$base" || { echo "test-flake: tools/test-affected changed-tests failed" >&2; return 2; }
}

# Whether the pinned forge has the gate: its help names the verb. (An older
# forge answers `forge test flakes gate --help` with `forge test`'s own help,
# and exit 0.)
if scripts/forge.sh test flakes gate --help 2>/dev/null | grep -q 'forge test flakes gate'; then
  log=$(mktemp)
  trap 'rm -f "$log"' EXIT
  rc=0
  scripts/forge.sh test flakes gate --base "$base" -- scripts/go-test.sh 2>&1 | tee "$log"
  rc=${PIPESTATUS[0]}
  case "$rc" in
    0) ;;
    1) status=1 ;;
    2)
      if grep -q '^flake gate: could not run' "$log"; then
        status=1 # the test command did not start: a failure, as it always was
      elif grep -q '^flake gate: could not identify' "$log"; then
        exit 2 # the diff could not be read: as tools/test-affected failing was
      fi ;; # otherwise no new or edited tests in the root module
    *) echo "test-flake: forge test flakes gate exited $rc" >&2; status=1 ;;
  esac
  # A nested module's tests (sdk/go), from that module.
  if git diff --name-only "$base" -- 'sdk/go/*_test.go' | grep -q . || git ls-files --others --exclude-standard -- 'sdk/go/*_test.go' | grep -q .; then
    targets=$(changed_tests) || exit 2
    while IFS=$'\t' read -r pkg pattern mod; do
      [ -n "$pkg" ] && [ -n "$mod" ] || continue
      repeat "$pkg" "$pattern" "$mod"
    done <<<"$targets"
  fi
  exit "$status"
fi

targets=$(changed_tests) || exit 2
if [ -z "$targets" ]; then
  echo "test-flake: no new or changed Go tests"
  exit 0
fi
# A third column names a nested Go module (sdk/go): its package path is relative
# to that module, and it is tested from there, outside any workspace, as its
# consumers build it. The root module does not contain it.
while IFS=$'\t' read -r pkg pattern mod; do
  [ -n "$pkg" ] || continue
  repeat "$pkg" "$pattern" "$mod"
done <<<"$targets"
exit "$status"
