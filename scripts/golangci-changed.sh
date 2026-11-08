#!/usr/bin/env bash
# golangci-changed.sh [--base <rev>] [--plan]   (task ci:golangci -- ...)
#
# golangci-lint over the Go packages the diff <rev>..HEAD changed, reporting
# only findings in the files that diff changed (--new-from-rev --whole-files),
# with the root .golangci.yml. Whole files, not only changed lines: a linter
# that reports a block on its first line (forge #1930's nestif grew a block by
# its inner lines) is missed line by line, and main is lint-clean (the release
# gate's whole-module lint holds it there), so a changed file carries no
# backlog. The merge queue's `golangci (changed)` job runs it
# (.github/workflows/merge-queue.yml), beside `CI result`, which fails unless it
# passed (owner, 2026-11-08: main went red three times in a day on staticcheck
# and gosec findings that only the release gate's whole-module lint saw).
#
# What it lints: every directory holding a changed .go file that is still a
# package of THIS module. Deleted directories, the root (its one file is
# generated), testdata, directories the go tool ignores (_x, .x) and the
# nested sdk/go module are dropped; a directory
# whose files are all behind build tags is dropped by `go list` (golangci-lint
# loads no tags, as the release gate's plain pass does not). No such package:
# it says so and exits 0 without loading anything.
#
# What it does not: packages the diff did not touch, findings in files it did
# not change, and the build-tagged pass.
# The release gate's unscoped `forge lint` still runs all of it on main.
#
# GOFLAGS=-trimpath: golangci-lint loads its packages through `go list
# -export`, whose compiles then share action IDs with the -trimpath compiles
# in main's Go build cache (ci.yml's go-cache job), so a dependency the diff did
# not reach is a cache hit rather than a compile.
#
# --base defaults to the merge base with origin/main. --plan prints the
# packages and exits; nothing is loaded.
set -euo pipefail

base="" plan=0
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base="${2:?--base needs a revision}"; shift 2 ;;
    --base=*) base="${1#--base=}"; shift ;;
    --plan) plan=1; shift ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) echo "golangci-changed: unknown argument: $1" >&2; exit 2 ;;
  esac
done
cd "$(git rev-parse --show-toplevel)"
# Locally, with no --base: what this branch adds over origin/main.
[ -n "$base" ] || base="$(git merge-base origin/main HEAD)"
git cat-file -e "${base}^{commit}" 2>/dev/null || { echo "golangci-changed: base ${base} is not a commit here (fetch it, or check out with fetch-depth: 0)" >&2; exit 2; }

# Directories with a changed (added, copied, modified, renamed) .go file, by
# the merge gate's own rule (scripts/lint-scope.sh: existing directories, not
# the root, not the nested sdk/go module), minus what the go tool ignores.
# Every discovery step's output is captured and its exit status checked before
# it is read: a step that fails (a process substitution's status is lost) would
# otherwise read as an empty scope and pass with "nothing to lint".
changed="$(git diff --name-only --diff-filter=ACMR "$base" HEAD -- '*.go')" ||
  { echo "golangci-changed: git diff ${base} HEAD failed" >&2; exit 2; }
scope=""
if [ -n "$changed" ]; then
  scope="$(printf '%s\n' "$changed" | scripts/lint-scope.sh)" ||
    { echo "golangci-changed: scripts/lint-scope.sh failed" >&2; exit 2; }
fi
dirs=()
while IFS= read -r d; do
  [ -n "$d" ] || continue
  case "/$d/" in */testdata/*|*/_*|*/.*) continue ;; esac
  dirs+=("./$d")
done <<<"${scope//,/$'\n'}"

pkgs=()
if [ "${#dirs[@]}" -gt 0 ]; then
  # A directory whose every file a build tag excludes is no package to lint.
  # -e keeps a broken package in the list (golangci-lint then reports it); a
  # go list that still exits non-zero could not load the module at all.
  listed="$(go list -e -f '{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}' "${dirs[@]}")" ||
    { echo "golangci-changed: go list over ${#dirs[@]} changed director(ies) failed; refusing to report an empty scope" >&2; exit 2; }
  while IFS= read -r p; do [ -n "$p" ] && pkgs+=("$p"); done < <(
    printf '%s\n' "$listed" | sed "s|^$(pwd -P)/|./|" | sort -u)
fi

if [ "${#pkgs[@]}" -eq 0 ]; then
  echo "golangci-changed: no Go package of this module changed since ${base}; nothing to lint"
  exit 0
fi
echo "golangci-changed: ${#pkgs[@]} changed package(s) since ${base}:"
printf '  %s\n' "${pkgs[@]}"
[ "$plan" -eq 0 ] || exit 0

command -v golangci-lint >/dev/null || { echo "golangci-changed: golangci-lint is not on PATH (CI: the setup action's golangci input)" >&2; exit 2; }
golangci-lint version
GOFLAGS="${GOFLAGS:+${GOFLAGS} }-trimpath" exec golangci-lint run --new-from-rev="$base" --whole-files "${pkgs[@]}"
