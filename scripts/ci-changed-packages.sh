#!/usr/bin/env bash
# The Go packages a change reaches: the packages BASE..HEAD changed, plus
# their direct importers. ci.yml's `windows-changed` job runs their -short
# tests on a native Windows runner in the merge queue (and on a pull request
# labelled ci:windows).
#
#   scripts/ci-changed-packages.sh [BASE]
#       BASE defaults to $CI_BASE, else the merge base of HEAD and origin/main.
#       Prints import paths, one per line, sorted; nothing when the change
#       reaches no package.
#
# WHY. On 2026-11-13 main's Windows job went red four times, each for a
# Windows-only fault in code a single pull request had just changed (chmod,
# HOME and symlink tests; an open file handle; path separators; stat error
# text and a 1ns DialTimeout). `GOOS=windows go vet` caught none of them; only
# running the tests on Windows does. The whole Windows -short tier takes 9-11
# minutes, so the merge queue runs the packages a change can break directly.
#
# WHAT A CHANGE REACHES. Every changed path (added, modified, deleted, either
# side of a rename) belongs to the nearest directory at or above it that is a
# package of this module: a .go file to its own package, a testdata fixture or
# an embedded template to the package that reads it. A path whose only
# enclosing package is the module root counts for the root only when it sits
# at the root itself, so docs/, .github/ and the like reach nothing. A change
# to go.mod or go.sum reaches every package.
#
# Then every package that imports one of those, from its code or its tests
# (Imports, TestImports, XTestImports), joins them: one level, not the
# transitive closure. A package two imports away is main's Windows job's to
# find.
#
# The guards (scripts/ci-test-packages.sh guards) are dropped, as main's
# Windows job drops them: whole-repo work, platform-independent, gated on
# Linux.
#
# `go list` runs on the host, so on the Windows runner it sees the Windows
# build: a directory whose every file a build constraint excludes there is no
# package to test. A failing `go list` fails the script; it never reads as
# "nothing to test".
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
if [[ -n "${1:-}" ]]; then
  base=$1
elif [[ -n "${CI_BASE:-}" ]]; then
  base=$CI_BASE
else
  base=$(git merge-base origin/main HEAD) || { echo "ci-changed-packages: no merge base with origin/main; pass BASE" >&2; exit 2; }
fi
git rev-parse -q --verify "$base^{commit}" >/dev/null || { echo "ci-changed-packages: cannot resolve base '$base' (fetch it)" >&2; exit 2; }

mapfile -t files < <(git diff --name-only --no-renames "$base" HEAD)
((${#files[@]})) || exit 0

module=$(go list -m) || { echo "ci-changed-packages: go list -m failed" >&2; exit 2; }
# One `go list` for the package set and the import graph: "<path> <import>...".
graph=$(go list -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}' ./...) ||
  { echo "ci-changed-packages: go list failed; cannot tell what the change reaches" >&2; exit 2; }

declare -A by_dir=() # module-relative directory ("." for the root) -> import path
while read -r path _; do
  [[ -n "$path" ]] || continue
  rel=${path#"$module"}
  rel=${rel#/}
  by_dir[${rel:-.}]=$path
done <<<"$graph"

declare -A guard=()
while read -r g; do [[ -n "$g" ]] && guard[$g]=1; done < <(scripts/ci-test-packages.sh guards)

declare -A changed=()
for f in "${files[@]}"; do
  case "$f" in
    go.mod | go.sum)
      for p in "${by_dir[@]}"; do changed[$p]=1; done
      break
      ;;
  esac
  if [[ "$f" != */* ]]; then
    [[ -n "${by_dir[.]:-}" ]] && changed[${by_dir[.]}]=1
    continue
  fi
  d=${f%/*}
  while :; do
    if [[ -n "${by_dir[$d]:-}" ]]; then
      changed[${by_dir[$d]}]=1
      break
    fi
    [[ "$d" == */* ]] || break # only the root is left: not reached (see the header)
    d=${d%/*}
  done
done

declare -A reached=()
for p in "${!changed[@]}"; do reached[$p]=1; done
while read -r path imports; do
  [[ -n "$path" && -z "${reached[$path]:-}" ]] || continue
  for i in $imports; do
    if [[ -n "${changed[$i]:-}" ]]; then
      reached[$path]=1
      break
    fi
  done
done <<<"$graph"

for p in "${!reached[@]}"; do
  [[ -n "${guard[$p]:-}" ]] || echo "$p"
done | sort
