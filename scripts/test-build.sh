#!/usr/bin/env bash
# test-build.sh [-p N] [--exclude FILE] — compile, never link and never run,
# every test package of the module: `go list -test -export` over each package
# with test files, and the packages whose tests sit behind a single custom
# build tag (e2e/stories' `e2e`, replaytest's `replayfixtures`, ...), each
# under its tag. A test file that no longer compiles fails it, naming the file
# and line.
#
# Why: the merge gate tests only the packages a change reaches within its
# budget (dev-docs/merge-and-release-gates.md), and its build phase compiles
# production code, not tests. A required field or a signature change then
# breaks a fixture in a test package nothing selected, and main finds it at
# the release gate: #3489 was 30 hours of red main, one refactor after
# another breaking fixtures in packages more than one import away. Compiling
# every test package catches each that stops compiling, in seconds on a warm
# build cache. It does not catch a fixture that compiles and fails at run
# time ("Deps.Projects is required" in a slow test, #3448/#3507); one that
# leaves a Deps field unset is internal/architecture's
# TestEveryConstructionSetsEveryRequiredDepsField.
#
# Not `go test -run '^$'`: that links every test binary (~2.7 GB RSS each) and
# runs each TestMain, which for the DB-backed packages boots pgtest. `go list
# -export` type-checks and compiles each package for its export data, exactly
# what a compile error needs, and its compiles are the ones `go test` reuses.
#
# Two callers, one list: the merge gate's testbuild phase, and `task
# build:packages`, which main's go-cache job runs before saving the Go build
# cache every CI job restores, so the merge queue recompiles only the test
# packages its change reaches. GOFLAGS=-trimpath, as everywhere a cache is
# shared (.github/actions/setup).
#
# Only packages with test files are listed: a package without one has no
# test variant, and its own compile is the build phase's.
#
# -p N bounds go's build parallelism (the gate's CPU budget); default: go's.
# --exclude FILE skips the untagged tests of the packages FILE lists (./dir,
# one per line), which another step compiles anyway: the merge gate passes the
# packages its test phase runs, whose `go test` compiles each, so the two never
# compile one test package twice side by side (after an internal/db change the
# whole set is ~1300 CPU-seconds). A line `./...` skips every untagged one (a
# FULL Go lane tests the module).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
p=() exclude=""
while [ $# -gt 0 ]; do
  case "$1" in
    -p) p=(-p "${2:?-p needs a number}"); shift 2 ;;
    --exclude) exclude=${2:?--exclude needs a file}; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) echo "test-build: unknown argument $1" >&2; exit 2 ;;
  esac
done
export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-trimpath"
root=$(pwd -P)

# The untagged test packages, minus --exclude.
all=$(go list -e -f '{{if or .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}' ./... | sed "s|^${root}|.|" | sort -u) ||
  { echo "test-build: go list ./... failed; cannot list the test packages" >&2; exit 2; }
if [ -n "$exclude" ]; then
  [ -r "$exclude" ] || { echo "test-build: --exclude $exclude is not readable" >&2; exit 2; }
  if grep -qx '\./\.\.\.' "$exclude"; then all=""; else
    all=$(comm -23 <(printf '%s\n' "$all") <(sort -u "$exclude")); fi
fi

# Test files whose build constraint is one bare tag (`//go:build e2e`): their
# packages compile under that tag. OS and architecture tags are the default
# build's already; a compound constraint (`linux && cleanup_acceptance`) is
# left to the release gate.
skip='^(linux|darwin|windows|unix|freebsd|openbsd|netbsd|android|ios|js|wasip1|amd64|arm64|386|arm|wasm|ignore|tools|cgo|race)$'
tagged=$(git ls-files -z -- '*_test.go' | xargs -0 -r grep -H -m1 -E '^//go:build [A-Za-z0-9_]+$' 2>/dev/null |
  sed -E 's|^(.*)/[^/]*_test\.go://go:build ([A-Za-z0-9_]+)$|\2 ./\1|' | grep -Ev "^\S+ \./\S*(testdata|/_|/\.)" |
  awk -v skip="$skip" '$1 !~ skip' | sort -u || true)
# Each tag is its own go list (a tag also selects files in its packages'
# dependencies), all at the same time as the untagged one: each loads only
# its few packages, and one after another they were most of a warm run.
pids=() fail=0
untagged=$(printf '%s\n' "$all" | grep -c . || true)
if [ "$untagged" -gt 0 ]; then
  echo "test-build: go list -test -export over ${untagged} test package(s)$([ -n "$exclude" ] && echo ", the rest compiled by the test phase")"
  # shellcheck disable=SC2086 # one word per package
  go list -test -export ${p[@]+"${p[@]}"} $all >/dev/null & pids+=($!)
else
  echo "test-build: every untagged test package is compiled by the test phase"
fi
for tag in $(echo "$tagged" | awk 'NF { print $1 }' | sort -u); do
  pkgs=$(echo "$tagged" | awk -v t="$tag" '$1 == t { print $2 }' | tr '\n' ' ')
  echo "test-build: go list -tags $tag -test -export $pkgs"
  # shellcheck disable=SC2086 # one word per package
  go list -tags "$tag" -test -export ${p[@]+"${p[@]}"} $pkgs >/dev/null & pids+=($!)
done
for pid in ${pids[@]+"${pids[@]}"}; do wait "$pid" || fail=1; done
[ "$fail" = 0 ] || { echo "test-build: a test package does not compile (above)" >&2; exit 1; }
echo "test-build: every test package compiles"
