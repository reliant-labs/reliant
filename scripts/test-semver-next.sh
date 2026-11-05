#!/usr/bin/env bash
# Hermetic test of scripts/semver-next.sh over a throwaway git repository.
set -euo pipefail
SCRIPT="$(cd "$(dirname "$0")" && pwd)/semver-next.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0
g() { git -C "$T" -c user.name=t -c user.email=t@t -c commit.gpgsign=false "$@"; }
c() { g commit -q --allow-empty -m "$@"; }
expect() { # <want stdout> <want exit> <args...>
  local want="$1" code="$2" got rc=0; shift 2
  got="$(bash "$SCRIPT" --repo "$T" "$@" 2>"$T.err")" || rc=$?
  if [ "$got" != "$want" ] || [ "$rc" != "$code" ]; then
    echo "FAIL: $* -> '$got' exit $rc (want '$want' exit $code)"; cat "$T.err"; fail=1
  else echo "ok: $* -> '$got' exit $rc"; fi
}

g init -q -b main
c "chore: init"; g tag v1.4.2
c "fix(api): a fix (#1)"; c "docs: words (#2)"; c "not conventional at all"
expect v1.4.3 0
g tag rc/v1.4.3-rc.1; g tag v1.4.3-rc.1   # neither is a base
expect v1.4.3 0
c "feat(cli): a feature (#3)"
expect v1.5.0 0
expect v1.5.0 0 --base v1.4.2 --head HEAD
g tag v1.5.0; c "perf: faster (#4)"
expect v1.5.1 0
c "refactor(core)!: drop the old flag (#5)"
expect "" 3
grep -q '(#5)' "$T.err" || { echo "FAIL: breaking refusal does not name #5"; fail=1; }
g tag -d v1.5.0 >/dev/null; g tag v1.5.0 HEAD   # base past the break
c "fix: x (#6)" -m "BREAKING CHANGE: the y field is gone"
expect "" 3
grep -q '(#6)' "$T.err" || { echo "FAIL: footer refusal does not name #6"; fail=1; }
expect v1.6.0 0 --allow-breaking
expect v1.6.0 0 --breaking-since HEAD
expect "" 3 --breaking-since HEAD~1
c "feat: mentions BREAKING CHANGE: in prose only (#7)"
expect v1.6.0 0 --base-rev HEAD~1
expect "" 2 --base v9
exit "$fail"
