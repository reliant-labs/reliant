#!/usr/bin/env bash
# golangci-changed.sh [--base <rev>] [--plan] [--in-slot]   (task ci:golangci -- ...)
#
# golangci-lint over the Go packages the diff from <rev> changed, reporting
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
# The merge gate (scripts/merge-gate.sh) runs it too, as its golangci phase,
# with the gate's diff base (owner, 2026-11-18): a finding then fails `task
# ci:merge` before the queue's job ejects the PR. One script, so the two
# cannot disagree on the selection, the config or the release.
#
# The diff is the working tree against <rev>, untracked .go files included,
# which is what golangci-lint's --new-from-rev itself reads; on a clean
# checkout (CI, the queue) that is exactly <rev>..HEAD.
#
# Which golangci-lint: the pinned forge's templates.GolangciLintVersion, read
# from the forge module's source the way the setup action reads it, or
# GOLANGCI_LINT_VERSION when set. A golangci-lint on PATH that reports another
# release fails the script (exit 2) and names the install command: a
# different release reports different findings (v2.14.0 turned months-old
# lines red), so a local pass would not predict the queue's.
#
# golangci-lint's machine-wide lock: without a flag, a second golangci-lint
# anywhere on the machine (another agent's forge lint, an editor) makes it
# exit 3 after five seconds, "parallel golangci-lint is running". So it waits
# for the lock (--allow-serial-runners), or, with --in-slot (a caller holding a
# heavy slot of its own, which already bounds memory, the lock's reason), drops
# it (--allow-parallel-runners), as forge lint does. Neither changes a finding.
#
# --base defaults to the merge base with origin/main. --plan prints the
# packages and exits; nothing is loaded.
set -euo pipefail

base="" plan=0 lock=--allow-serial-runners
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base="${2:?--base needs a revision}"; shift 2 ;;
    --base=*) base="${1#--base=}"; shift ;;
    --plan) plan=1; shift ;;
    --in-slot) lock=--allow-parallel-runners; shift ;;
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
changed="$(git diff --name-only --diff-filter=ACMR "$base" -- '*.go' && git ls-files --others --exclude-standard -- '*.go')" ||
  { echo "golangci-changed: git diff ${base} failed" >&2; exit 2; }
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

pin="${GOLANGCI_LINT_VERSION:-}"
if [ -z "$pin" ]; then
  # As the setup action's "Read the golangci-lint pin" step reads it.
  fdir="$(go mod download -json github.com/reliant-labs/forge | jq -r .Dir)" ||
    { echo "golangci-changed: cannot locate the pinned forge module to read its golangci-lint pin" >&2; exit 2; }
  pin="$(sed -n 's/^const GolangciLintVersion = "v\([0-9][0-9.]*\)"$/\1/p' "${fdir}/internal/templates/toolversions.go" 2>/dev/null)"
  [ -n "$pin" ] || { echo "golangci-changed: no GolangciLintVersion in ${fdir}/internal/templates/toolversions.go (the pinned forge moved it?)" >&2; exit 2; }
fi
pin="${pin#v}"
install="go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${pin}"
command -v golangci-lint >/dev/null || { echo "golangci-changed: golangci-lint is not on PATH; install the pinned v${pin}: ${install} (CI: the setup action's golangci input)" >&2; exit 2; }
ver="$(golangci-lint version 2>&1)" || true
echo "$ver"
case "$ver" in
  *"has version ${pin} "*|*"has version v${pin} "*|*"has version ${pin}") ;;
  *) echo "golangci-changed: $(command -v golangci-lint) is not golangci-lint ${pin}, the pinned forge's release that the merge queue lints with; install it: ${install}" >&2; exit 2 ;;
esac
GOFLAGS="${GOFLAGS:+${GOFLAGS} }-trimpath" exec golangci-lint run "$lock" --new-from-rev="$base" --whole-files "${pkgs[@]}"
