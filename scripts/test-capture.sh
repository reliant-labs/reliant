#!/usr/bin/env bash
# test-capture.sh — run one test lane through `forge test capture`, so its
# results and coverage are recorded under .forge/tests/runs/<run>/<lane>/.
#
#   scripts/test-capture.sh [--raw] go <lane> <go test flags and packages...>
#   scripts/test-capture.sh vitest <lane> <vitest command...>
#
# reliant dogfoods forge's Tests product: the merge gate's affected lanes
# (tools/test-affected run -capture: lanes short and web-affected), the
# release gate's `task test` (unit), `task test:web` (frontend),
# `task test:integration` (integration) and `task test:e2e` (e2e), and the
# flake gate (scripts/test-flake.sh, lane flake-gate) are recorded.
# `forge test report` summarises a commit's runs; on main CI uploads them
# (ci.yml's test-upload job).
#
# THE VERDICT IS THE COMMAND'S. Capture runs the command unchanged (no
# shell), passes stdin and the environment through and exits with its exit
# status, always. A record it cannot write is a warning on stderr. What this
# script adds to the command is only what the record needs:
#
#   go      -json (capture renders the stream back into the text `go test`
#           prints, -v's when the command passes -v; --raw prints the stream
#           itself, for a caller that reads it: tools/test-affected's
#           per-test budget filter), and -covermode=atomic
#           -coverprofile=<lane dir>/cover.out. -coverprofile is a cacheable
#           flag, so a -short lane replays from Go's test cache as before.
#           Go runs through scripts/go-test.sh, which drops the FORGE_TEST_*
#           variables capture exports, so the cache never keys on them.
#   vitest  --results <lane dir>/vitest.json: frontends/web/vite.config.ts
#           sees FORGE_TEST_LANE_DIR and writes its JSON results and its v8
#           coverage (coverage-final.json) into the lane directory.
#
# FORGE_TEST_COVERAGE=off records results only (no cover flags; Vitest's
# coverage off); the merge gate's lanes (tools/test-affected run -capture)
# set it, so their builds and results are the plain ones task test:short
# shares. FORGE_TESTS=off, or a forge that cannot begin a run (a
# `go run` of the pin with no module proxy, an unwritable checkout), runs the
# command exactly as it would run unrecorded (plain `go test`, with -json
# under --raw, or the vitest command): recording never breaks a lane.
#
# The run is $FORGE_TEST_RUN when set (an outer gate began one, so its lanes
# form one run), else a new one. Lane names: letters, digits, '-' and '_',
# unique within a run.
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
raw=0
if [ "${1:-}" = --raw ]; then raw=1; shift; fi
kind=${1:-} lane=${2:-}
if [ $# -lt 3 ] || { [ "$kind" != go ] && [ "$kind" != vitest ]; }; then
  echo "usage: $0 [--raw] go <lane> <go test args...> | $0 vitest <lane> <command...>" >&2
  exit 2
fi
shift 2

# plain: the lane, unrecorded.
plain() {
  if [ "$kind" = go ]; then
    if [ "$raw" = 1 ]; then exec "$root/scripts/go-test.sh" -json "$@"; fi
    exec "$root/scripts/go-test.sh" "$@"
  fi
  exec "$@"
}

[ "${FORGE_TESTS:-}" != off ] || plain "$@"
# begin prints the run directory (joining $FORGE_TEST_RUN), and a scratch
# directory when it cannot record; a forge that has no `test begin` fails.
run=$("$root/scripts/forge.sh" test begin 2>/dev/null) && [ -n "$run" ] && [ -d "$run" ] || {
  echo "test-capture: forge could not begin a test run; lane $lane runs unrecorded" >&2
  plain "$@"
}
dir="$run/$lane"

# relocate <lane dir>: make a Vitest lane's record portable. Istanbul's
# coverage-final.json keys every file, and its "path", by ABSOLUTE source path,
# and vitest.json names each test file absolutely. A record read in another
# checkout of the same commit (ci.yml's test-upload job reads the release
# gate's private snapshot's runs) keeps those snapshot paths: no repo path, no
# git blob id, no diff coverage, and test ids that differ per checkout. So
# the checkout's root is cut off them here, leaving repo-relative paths, which
# `forge test report` places in any checkout (forge's own fix to relocate a
# foreign root, forge#2694, covers coverage). Go lanes need nothing: a
# coverprofile names files by import path. Never changes the lane's verdict:
# without jq, or on a file jq cannot read, the record stays as Vitest wrote
# it, with a warning.
relocate() {
  local d=$1 f tmp top phys
  top=$(git -C "$root" rev-parse --show-toplevel 2>/dev/null) || top=$root
  phys=$(cd "$top" && pwd -P)
  command -v jq >/dev/null 2>&1 || { echo "test-capture: no jq: lane $lane's Vitest paths stay absolute" >&2; return 0; }
  for f in coverage-final.json vitest.json; do
    [ -s "$d/$f" ] || continue
    tmp="$d/.$f.tmp"
    if jq -c --arg a "$top/" --arg b "$phys/" --arg f "$f" '
        def rel: if type == "string" then ltrimstr($a) | ltrimstr($b) else . end;
        if $f == "coverage-final.json" then with_entries(.key |= rel | .value.path |= rel)
        elif (.testResults | type) == "array" then .testResults |= map(.name |= rel)
        else . end' "$d/$f" >"$tmp"; then
      mv -f "$tmp" "$d/$f"
    else
      rm -f "$tmp"
      echo "test-capture: could not relocate $d/$f; its paths stay absolute" >&2
    fi
  done
}

capture=("$root/scripts/forge.sh" test capture --run "$run" --lane "$lane")
case "$kind" in
  go)
    [ "$raw" = 1 ] && capture+=(--render raw)
    cover=()
    [ "${FORGE_TEST_COVERAGE:-}" = off ] || cover=(-covermode=atomic "-coverprofile=$dir/cover.out")
    exec "${capture[@]}" -- "$root/scripts/go-test.sh" -json ${cover[@]+"${cover[@]}"} "$@" ;;
  vitest)
    status=0
    "${capture[@]}" --results "$dir/vitest.json" --format vitest -- "$@" || status=$?
    relocate "$dir"
    exit "$status" ;;
esac
