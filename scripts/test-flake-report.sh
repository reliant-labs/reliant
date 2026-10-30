#!/usr/bin/env bash
# Hermetic self-test of scripts/flake-report.sh's verdicts, against a throwaway
# Go module and a local gate log directory (--logs, --dry-run: no gh, no issue).
#
#   - a test that failed once and passes on retry is a suspected flake;
#   - a deterministic data race is never one: when the original log shows the
#     race detector's report it is not retried, and when it does not, the retry
#     runs under -race (as main's lanes do) and fails again.
#
# Needs go with cgo (the race detector). Run it from anywhere.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/flake-report.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT

mod="$fx/mod"
mkdir -p "$mod/racy" "$mod/flaky"
printf 'module example.com/fx\n\ngo 1.22\n' >"$mod/go.mod"
cat >"$mod/racy/racy_test.go" <<'GO'
package racy

import (
	"sync"
	"testing"
)

// Passes without -race, fails under it: two goroutines write n unsynchronised.
func TestRacy(t *testing.T) {
	n := 0
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); n++ }()
	}
	wg.Wait()
	_ = n
}
GO
cat >"$mod/flaky/flaky_test.go" <<'GO'
package flaky

import "testing"

// Passes every time it runs here; the log below says it failed once on main.
func TestSometimes(t *testing.T) {}
GO

logs="$fx/logs"
mkdir -p "$logs"
run() { # <log body> -> the script's output
  printf '%b' "$1" >"$logs/test.log"
  (cd "$mod" && GITHUB_REPOSITORY=fx/fx GOFLAGS= bash "$src" --logs "$logs" --sha deadbeef --dry-run 2>&1)
}
fail() { echo "FAIL: $*" >&2; exit 1; }

out="$(run '--- FAIL: TestSometimes (0.00s)\n    flaky_test.go:6: boom\nFAIL\texample.com/fx/flaky\t0.01s\n')"
grep -q 'TestSometimes PASSED on retry: suspected flake' <<<"$out" || fail "a failed-then-passing test is a suspected flake: $out"

out="$(run '=== RUN   TestRacy\n==================\nWARNING: DATA RACE\n==================\n    testing.go:1490: race detected during execution of test\n--- FAIL: TestRacy (0.00s)\nFAIL\texample.com/fx/racy\t0.01s\n')"
grep -q 'TestRacy failed on the race detector: a real failure' <<<"$out" || fail "a race-detector failure is a real failure: $out"
! grep -q 'PASSED on retry' <<<"$out" || fail "a data race was reported as a flake: $out"

# The original log lost the race report: the -race retry still catches it.
out="$(run '--- FAIL: TestRacy (0.00s)\nFAIL\texample.com/fx/racy\t0.01s\n')"
grep -q 'TestRacy failed again' <<<"$out" || fail "the retry runs under -race and fails again: $out"
! grep -q 'PASSED on retry' <<<"$out" || fail "a race-only failure passed a non-race retry: $out"

echo "flake-report self-test: ok"
