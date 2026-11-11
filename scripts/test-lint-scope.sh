#!/usr/bin/env bash
# test-lint-scope.sh — scripts/lint-scope.sh against throwaway git repos: a
# nested Go module and everything under it leave the scope, ordinary root-module
# directories and a sibling that only shares the module's prefix stay, deleted
# directories and root files drop out, and a checkout with no nested module
# keeps every changed directory.
#
#   scripts/test-lint-scope.sh        (seconds; needs only git, bash, awk)
set -euo pipefail
script=$(cd "$(dirname "$0")" && pwd)/lint-scope.sh
tmp=$(mktemp -d "${TMPDIR:-/tmp}/test-lint-scope.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

repo() { # dir, then the files to create and commit
  local d=$tmp/$1; shift
  git init -q "$d"
  for f in "$@"; do mkdir -p "$d/$(dirname "$f")"; : >"$d/$f"; done
  git -C "$d" add -A && git -C "$d" commit -qm base
  echo "$d"
}
check() { # repo, want, then the changed files
  local d=$1 want=$2 got; shift 2
  got=$(cd "$d" && printf '%s\n' "$@" | "$script")
  [ "$got" = "$want" ] || fail "$(basename "$d"): changed [$*]: got '$got', want '$want'"
}

# A root module with a nested one at sdk/go.
w=$(repo nested go.mod sdk/go/go.mod sdk/go/reliant/a.go sdk/go/b.go \
  sdk/gox/c.go sdk/d.go internal/x/e.go internal/y/f.go)
check "$w" "internal/x,internal/y,sdk,sdk/gox" \
  sdk/go/b.go sdk/go/reliant/a.go sdk/go/go.mod sdk/gox/c.go sdk/d.go \
  internal/x/e.go internal/y/f.go internal/x/e.go go.mod Taskfile.yml
# Only the nested module changed: nothing to lint.
check "$w" "" sdk/go/reliant/a.go sdk/go/go.sum
# A directory the change deleted is not a pattern forge lint can load.
check "$w" "internal/x" internal/x/e.go internal/gone/g.go

# No nested module: every changed directory stays, sdk/go included.
w=$(repo flat go.mod sdk/go/reliant/a.go internal/x/e.go)
check "$w" "internal/x,sdk/go/reliant" sdk/go/reliant/a.go internal/x/e.go go.mod
check "$w" "" go.mod

echo "PASS: lint-scope.sh"
