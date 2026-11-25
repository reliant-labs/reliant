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
rc=0
GOFLAGS="${GOFLAGS:+${GOFLAGS} }-trimpath" golangci-lint run "$lock" --new-from-rev="$base" --whole-files "${pkgs[@]}" || rc=$?

# ── deprecations: staticcheck's SA1019 where the diff deprecates ────────────
# A declaration this diff marks `Deprecated:` (by hand, or in the code a proto
# `deprecated = true` generates) turns every use of it into an SA1019 finding,
# in files the diff did not change, so the pass above, which reports only the
# changed files, never sees them; the release gate's whole-module lint did
# (#3005: #2930 deprecated ListMessagesResponse.Total, and
# internal/handlers/chat/rpc_list_messages.go, untouched, still wrote it). So
# for each declaration the diff deprecates, staticcheck runs over the packages
# that declare or import it and name it, and fails on an SA1019 finding
# there. Usually none: then it costs only the scan.
#
# deprecated_names prints "<dir> <name>" for each: the first identifier of the
# declaration a `Deprecated:` line the diff adds documents (the next code line
# after the comment, or the same line for a trailing comment). A deprecated
# block (`const (`) names nothing and is left to the release gate.
deprecated_names() {
  local f lines
  for f in $changed; do
    case "$f" in sdk/go/*|*/testdata/*) continue ;; esac
    [ -f "$f" ] || continue
    if git cat-file -e "${base}:${f}" 2>/dev/null; then
      lines=$(git diff -U0 "$base" -- "$f" | awk '
        /^@@/ { split($3, a, ","); n = substr(a[1], 2) + 0; next }
        /^\+\+\+/ { next }
        /^\+/ { if ($0 ~ /Deprecated:/) print n; n++; next }
        /^-/ { next }')
    else
      lines=$(grep -n 'Deprecated:' "$f" | cut -d: -f1 || true)
    fi
    [ -n "$lines" ] || continue
    # shellcheck disable=SC2086 # one word per line number
    awk -v lines=" $(printf '%s ' $lines)" -v dir="$(dirname "$f")" '
      function name(s) {
        if (match(s, /^[[:space:]]*func[[:space:]]*(\([^)]*\)[[:space:]]*)?[A-Za-z_][A-Za-z0-9_]*/)) { s = substr(s, 1, RLENGTH); sub(/.*[^A-Za-z0-9_]/, "", s); return s }
        if (s ~ /^[[:space:]]*(type|var|const)[[:space:]]*\(/) return ""
        if (match(s, /^[[:space:]]*(type|var|const)[[:space:]]+[A-Za-z_][A-Za-z0-9_]*/)) { s = substr(s, 1, RLENGTH); sub(/.*[^A-Za-z0-9_]/, "", s); return s }
        if (match(s, /^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*/)) { s = substr(s, 1, RLENGTH); gsub(/[[:space:]]/, "", s); return s }
        return ""
      }
      index(lines, " " NR " ") {
        if ($0 !~ /^[[:space:]]*\/\//) { n = name($0); if (n != "") print dir, n; next }
        want = 1; next
      }
      want && ($0 ~ /^[[:space:]]*\/\// || $0 ~ /^[[:space:]]*$/) { next }
      want { n = name($0); if (n != "") print dir, n; want = 0 }
    ' "$f"
  done | sort -u
}
deps=$(deprecated_names)
if [ -n "$deps" ]; then
  names=$(echo "$deps" | awk '{ print $2 }' | sort -u | paste -sd'|' -)
  echo "golangci-changed: the diff deprecates: $(echo "$deps" | awk '{ print $1 "." $2 }' | tr '\n' ' ')"
  declared=$(echo "$deps" | awk '{ print "./" $1 }' | sort -u)
  # shellcheck disable=SC2086 # one word per directory
  dep_paths=$(go list -e -f '{{.ImportPath}}' $declared | sort -u) ||
    { echo "golangci-changed: go list over the deprecating packages failed" >&2; exit 2; }
  graph=$(go list -e -f '{{.Dir}}{{"\t"}}{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./...) ||
    { echo "golangci-changed: go list ./... failed; cannot find who uses what the diff deprecates" >&2; exit 2; }
  users=()
  while IFS=$'\t' read -r dir imports; do
    [ -n "$dir" ] || continue
    hit=0
    for p in $dep_paths; do case " $imports " in *" $p "*) hit=1; break ;; esac; done
    [ "$hit" = 1 ] || continue
    # Only a package whose source names a deprecated identifier can use it.
    grep -qwE "$names" "$dir"/*.go 2>/dev/null || continue
    users+=("$(printf '%s\n' "$dir" | sed "s|^$(pwd -P)/|./|")")
  done <<<"$graph"
  if [ "${#users[@]}" -eq 0 ]; then
    echo "golangci-changed: no package uses what the diff deprecates"
  else
    echo "golangci-changed: staticcheck SA1019 over ${#users[@]} package(s) that use it:"
    printf '  %s\n' "${users[@]}"
    report=$(mktemp)
    drc=0
    GOFLAGS="${GOFLAGS:+${GOFLAGS} }-trimpath" golangci-lint run "$lock" --enable-only staticcheck \
      --output.text.path /dev/null --output.json.path "$report" "${users[@]}" || drc=$?
    if [ "$drc" -gt 1 ] || ! jq -e 'has("Issues")' "$report" >/dev/null 2>&1; then
      echo "golangci-changed: the SA1019 pass could not run (golangci-lint exit ${drc})" >&2
      rm -f "$report"; exit 2
    fi
    found=$(jq -r '.Issues[]? | select(.Text | startswith("SA1019")) | "\(.Pos.Filename):\(.Pos.Line):\(.Pos.Column): \(.Text) (staticcheck)"' "$report")
    rm -f "$report"
    if [ -n "$found" ]; then
      echo "$found"
      echo "golangci-changed: the diff deprecates what these lines still use (SA1019): move them off it, or keep a justified //nolint:staticcheck // SA1019 where the use must stay (an old client's field)" >&2
      [ "$rc" != 0 ] || rc=1
    fi
  fi
fi
exit "$rc"
