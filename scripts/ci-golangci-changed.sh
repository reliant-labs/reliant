#!/usr/bin/env bash
# golangci-lint over the Go packages BASE..HEAD changed, reporting only the
# findings in the files that diff changed (--new-from-rev --whole-files: a
# linter that reports a block on its first line, nestif in #1930, is missed
# when only the lines inside it changed; main's Lint job is whole-repo, so a
# changed file carries no backlog). ci.yml's
# `golangci` job runs it in the merge tier, beside test-short and checks, and
# `CI gate` needs it (owner, 2026-11-08: main went red on lint findings only
# the main-only Lint job saw, #1931 and #1930; issue #1947).
#
#   scripts/ci-golangci-changed.sh [--plan] [BASE]
#       BASE defaults to $CI_BASE, else the merge base of HEAD and origin/main.
#       --plan prints the packages and exits; nothing is loaded.
#
# Two configs, as the Lint job on main runs them. A changed package outside
# pkg/ is linted from the repo root under .golangci.yml; one under pkg/ (which
# the root config excludes) from pkg/, under pkg/.golangci.yml, which
# golangci-lint finds by walking up from its working directory. Both passes
# run, whatever the first one found.
#
# What it lints: every directory holding a changed .go file that is still a
# package of this module. Deleted directories, testdata (whose fixtures are
# often modules of their own), directories the go tool ignores (_x, .x), any
# directory inside a nested module, and a directory whose every file a build
# tag excludes (golangci-lint loads no tags, like the Lint job) are dropped.
# No such package: it says so and exits 0 before loading anything.
#
# What it leaves to the Lint job on main: packages the diff did not touch,
# findings in files it did not change, go vet and the -tags e2e vet.
set -euo pipefail

plan=0
if [[ "${1:-}" == --plan ]]; then plan=1; shift; fi
cd "$(git rev-parse --show-toplevel)"
if [[ -n "${1:-}" ]]; then
  base=$1
elif [[ -n "${CI_BASE:-}" ]]; then
  base=$CI_BASE
else
  base=$(git merge-base origin/main HEAD) || { echo "ci-golangci-changed: no merge base with origin/main; pass BASE" >&2; exit 2; }
fi
git rev-parse -q --verify "$base^{commit}" >/dev/null || { echo "ci-golangci-changed: cannot resolve base '$base' (fetch it)" >&2; exit 2; }

# Directories of changed (added, copied, modified, renamed) .go files, minus
# what the go tool ignores and anything under a nested go.mod. One awk, not a
# dirname per file.
nested=$(git ls-files '*/go.mod' | awk -F/ '{ NF--; print }' OFS=/ | paste -sd' ' -)
mapfile -t dirs < <(git diff --name-only --diff-filter=ACMR "$base" HEAD -- '*.go' |
  awk -F/ -v nested="$nested" '
    BEGIN { OFS = "/"; n = split(nested, mods, " ") }
    {
      if (NF == 1) { print "."; next }
      NF--
      if (("/" $0 "/") ~ /\/(testdata|_[^\/]*|\.[^\/]*)\//) next
      for (i = 1; i <= n; i++) if ($0 == mods[i] || index($0, mods[i] "/") == 1) next
      print
    }' | sort -u | while read -r d; do if [[ -d "$d" ]]; then echo "./$d"; fi; done)

pkgs=()
if ((${#dirs[@]})); then
  # A directory whose every file a build tag excludes is no package to lint.
  # -e reports such a directory as a package with an error and no files, so
  # the template drops it; anything that still fails go list (a toolchain it
  # cannot load, a broken go.mod) fails the gate rather than reading as
  # "nothing to lint". go list prints the physical directory; so does pwd -P.
  top=$(pwd -P)
  listed=$(go list -e -f '{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}' "${dirs[@]}") ||
    { echo "ci-golangci-changed: go list failed over the changed directories; cannot tell what to lint" >&2; exit 2; }
  mapfile -t pkgs < <(printf '%s\n' "$listed" | sed "s|^${top}\$|.|; s|^${top}/|./|" | awk 'NF' | sort -u)
fi
root=() lib=()
for p in "${pkgs[@]}"; do
  case "$p" in
    ./pkg | ./pkg/*) lib+=(".${p#./pkg}") ;;
    *) root+=("$p") ;;
  esac
done

if ((${#root[@]} + ${#lib[@]} == 0)); then
  echo "ci-golangci-changed: no Go package of this module changed since $base; nothing to lint"
  exit 0
fi
echo "ci-golangci-changed: since $base, ${#root[@]} package(s) under .golangci.yml, ${#lib[@]} under pkg/.golangci.yml"
((${#root[@]} == 0)) || printf '  %s\n' "${root[@]}"
((${#lib[@]} == 0)) || printf '  pkg/%s\n' "${lib[@]#./}"
((plan == 0)) || exit 0

command -v golangci-lint >/dev/null || { echo "ci-golangci-changed: golangci-lint is not on PATH (CI: install-release-tool golangci-lint)" >&2; exit 2; }
golangci-lint version
rc=0
if ((${#root[@]})); then
  echo "── golangci-lint (root, .golangci.yml)"
  golangci-lint run --new-from-rev="$base" --whole-files "${root[@]}" || rc=1
fi
if ((${#lib[@]})); then
  echo "── golangci-lint (pkg/, pkg/.golangci.yml)"
  (cd pkg && golangci-lint run --new-from-rev="$base" --whole-files "${lib[@]}") || rc=1
fi
exit "$rc"
