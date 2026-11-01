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

# The issues, against a fake gh that applies -q like the real one and logs
# every call: one issue per test, reopened rather than duplicated, and closed
# by --sweep once its test is gone or quiet for FLAKE_SWEEP_DAYS.
cat >>"$mod/flaky/flaky_test.go" <<'GO'

// Exists, but has not flaked for twelve days in the sweep's fixture.
func TestQuiet(t *testing.T) {}
GO
bin="$fx/bin"
mkdir -p "$bin"
cat >"$bin/gh" <<'SH'
#!/usr/bin/env bash
echo "$*" >>"$GH_LOG"
[[ "$1 $2" == "issue list" ]] || exit 0
q="" prev=""
for a in "$@"; do [[ "$prev" == -q ]] && q="$a"; prev="$a"; done
if [[ -n "$q" ]]; then jq -r "$q" "$GH_ISSUES"; else cat "$GH_ISSUES"; fi
SH
chmod +x "$bin/gh"
gh_run() { # <issues json> <args...> -> the script's output; calls in $fx/gh.log
  printf '%s' "$1" >"$fx/issues.json"
  shift
  : >"$fx/gh.log"
  (cd "$mod" && PATH="$bin:$PATH" GH_LOG="$fx/gh.log" GH_ISSUES="$fx/issues.json" GITHUB_REPOSITORY=fx/fx GOFLAGS= \
    FLAKE_SWEEP_NOW=1793491200 bash "$src" "$@" 2>&1) # now = 2026-11-01T00:00:00Z
}

printf '%b' '--- FAIL: TestSometimes (0.00s)\nFAIL\texample.com/fx/flaky\t0.01s\n' >"$logs/test.log"
out="$(gh_run '[{"number":7,"title":"flake:suspected: TestSometimes (flaky)","state":"CLOSED"}]' --logs "$logs" --sha deadbeef)"
grep -q '^issue reopen 7 ' "$fx/gh.log" || fail "a closed issue for the same test is reopened: $out $(cat "$fx/gh.log")"
grep -q '^issue comment 7 ' "$fx/gh.log" || fail "the new occurrence is commented on the reopened issue: $(cat "$fx/gh.log")"
! grep -q '^issue create' "$fx/gh.log" || fail "a second issue was created for one test: $(cat "$fx/gh.log")"

out="$(gh_run '[
  {"number":1,"title":"flake:suspected: TestSometimes (flaky)","createdAt":"2026-10-01T00:00:00Z",
   "comments":[{"body":"Failed then passed on retry again: run 9","createdAt":"2026-10-30T00:00:00Z"}]},
  {"number":2,"title":"flake:suspected: TestGone (flaky)","createdAt":"2026-10-31T00:00:00Z","comments":[]},
  {"number":3,"title":"flake:suspected: TestQuiet (flaky)","createdAt":"2026-10-20T00:00:00Z",
   "comments":[{"body":"Looking into it","createdAt":"2026-10-31T00:00:00Z"}]},
  {"number":4,"title":"flake:suspected: TestRacy (racy)","createdAt":"2026-10-30T00:00:00Z","comments":[]}
]' --sweep)"
grep -q '^issue close 2 .*TestGone. no longer exists in .flaky' "$fx/gh.log" || fail "a deleted test's issue is closed: $out"
grep -q '^issue close 3 .*not flaked again for 12 days' "$fx/gh.log" || fail "a test quiet for 12 days is closed (a human comment is no occurrence): $out"
[[ "$(grep -c '^issue close' "$fx/gh.log")" -eq 2 ]] || fail "only #2 and #3 are closed: $(cat "$fx/gh.log")"

echo "flake-report self-test: ok"
