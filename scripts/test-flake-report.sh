#!/usr/bin/env bash
# Hermetic self-test of scripts/flake-report.sh: no network, no real gh, no
# real forge.
#
#   - The rerun-and-file path is a wrapper over `forge test flakes confirm`
#     and `forge test flakes --issues`. A fake forge (FORGE_BIN) records its
#     argv, and the test pins what the wrapper passes for each source (--logs,
#     a run id, --list, --dry-run), and that a failing forge never fails the
#     script. The verdicts themselves (a retried pass is a flake, a data race
#     never is, one issue per test, reopened rather than duplicated) are
#     forge's, held by forge's internal/testflakes tests.
#   - --sweep is still the script's own: against a fake gh that applies -q like
#     the real one, it closes an issue whose test is gone or quiet for
#     FLAKE_SWEEP_DAYS, and no other.
#
# Run it from anywhere.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/flake-report.sh"
gotest="$(dirname "$src")/go-test.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

mod="$fx/mod"
mkdir -p "$mod/flaky"
printf 'module example.com/fx\n\ngo 1.22\n' >"$mod/go.mod"
cat >"$mod/flaky/flaky_test.go" <<'GO'
package flaky

import "testing"

func TestSometimes(t *testing.T) {}

// Exists, but has not flaked for twelve days in the sweep's fixture.
func TestQuiet(t *testing.T) {}
GO

bin="$fx/bin"
mkdir -p "$bin"
# The fake forge: one line per call, `forge <args>`; FORGE_FAKE_EXIT is its status.
cat >"$bin/forge" <<'SH'
#!/usr/bin/env bash
echo "$*" >>"$FORGE_LOG"
exit "${FORGE_FAKE_EXIT:-0}"
SH
cat >"$bin/gh" <<'SH'
#!/usr/bin/env bash
echo "$*" >>"$GH_LOG"
[[ "$1 $2" == "issue list" ]] || exit 0
q="" prev=""
for a in "$@"; do [[ "$prev" == -q ]] && q="$a"; prev="$a"; done
if [[ -n "$q" ]]; then jq -r "$q" "$GH_ISSUES"; else cat "$GH_ISSUES"; fi
SH
chmod +x "$bin/forge" "$bin/gh"

# run <args...> -> the script's output; forge's calls in $fx/forge.log.
run() {
  : >"$fx/forge.log"
  (cd "$mod" && FORGE_BIN="$bin/forge" FORGE_LOG="$fx/forge.log" PATH="$bin:$PATH" GH_LOG="$fx/gh.log" \
    GITHUB_REPOSITORY=fx/fx bash "$src" "$@" 2>&1)
}
calls() { cat "$fx/forge.log"; }

run --logs "$fx/logs" --sha deadbeef --dry-run >/dev/null
want="test flakes confirm --logs $fx/logs --sha deadbeef --repo fx/fx --max 15 -- $gotest
test flakes --issues --repo fx/fx --dry-run"
[[ "$(calls)" == "$want" ]] || fail "--logs --dry-run: forge was called as
$(calls)
want
$want"

MAX_TESTS=4 run 1234567890 >/dev/null
want="test flakes confirm --run 1234567890 --repo fx/fx --max 4 -- $gotest
test flakes --issues --repo fx/fx"
[[ "$(calls)" == "$want" ]] || fail "a run id: forge was called as
$(calls)
want
$want"

run 1234567890 --list >/dev/null
want="test flakes confirm --run 1234567890 --repo fx/fx --list --max 15 -- $gotest"
[[ "$(calls)" == "$want" ]] || fail "--list confirms and files nothing: forge was called as
$(calls)"

rc=0
out="$(FORGE_FAKE_EXIT=3 run 1234567890)" || rc=$?
[[ "$rc" -eq 0 ]] || fail "a failing forge failed the script (exit $rc): $out"
grep -q 'forge test flakes confirm exited 3' <<<"$out" || fail "a failing confirm is not reported: $out"
[[ "$(calls | wc -l | tr -d ' ')" -eq 2 ]] || fail "a failing confirm must still sync the issues: $(calls)"

# --sweep, against the fake gh.
sweep() { # <issues json> -> the script's output; gh's calls in $fx/gh.log
  printf '%s' "$1" >"$fx/issues.json"
  : >"$fx/gh.log"
  : >"$fx/forge.log"
  (cd "$mod" && FORGE_BIN="$bin/forge" FORGE_LOG="$fx/forge.log" PATH="$bin:$PATH" GH_LOG="$fx/gh.log" GH_ISSUES="$fx/issues.json" GITHUB_REPOSITORY=fx/fx \
    FLAKE_SWEEP_NOW=1793491200 bash "$src" --sweep 2>&1) # now = 2026-11-01T00:00:00Z
}
out="$(sweep '[
  {"number":1,"title":"flake:suspected: TestSometimes (flaky)","createdAt":"2026-10-01T00:00:00Z",
   "comments":[{"body":"Failed then passed on retry again: run 9","createdAt":"2026-10-30T00:00:00Z"}]},
  {"number":2,"title":"flake:suspected: TestGone (flaky)","createdAt":"2026-10-31T00:00:00Z","comments":[]},
  {"number":3,"title":"flake:suspected: TestQuiet (flaky)","createdAt":"2026-10-20T00:00:00Z",
   "comments":[{"body":"Looking into it","createdAt":"2026-10-31T00:00:00Z"}]}
]')"
grep -q '^issue close 2 .*TestGone. no longer exists in .flaky' "$fx/gh.log" || fail "a deleted test's issue is closed: $out"
grep -q '^issue close 3 .*not flaked again for 12 days' "$fx/gh.log" || fail "a test quiet for 12 days is closed (a human comment is no occurrence): $out"
[[ "$(grep -c '^issue close' "$fx/gh.log")" -eq 2 ]] || fail "only #2 and #3 are closed: $(cat "$fx/gh.log")"
[[ ! -s "$fx/forge.log" ]] || fail "--sweep must not call forge: $(calls)"

echo "flake-report self-test: ok"
