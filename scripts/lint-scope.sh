#!/usr/bin/env bash
# lint-scope.sh — the merge gate's forge lint scope: the changed files on stdin
# (one path per line, relative to the repository root, the working directory)
# become the comma-separated list of their existing parent directories.
#
# Root files (no directory) are dropped: the release gate lints the module.
# Directories inside a nested Go module (sdk/go has its own go.mod) are
# dropped too: forge lint loads packages from the root module, and its
# contract lane fails outright on a pattern the root module does not contain.
# The nested module's own build and tests cover it. A sibling that only shares
# a prefix (sdk/gox beside sdk/go) stays in scope.
#
# One awk, not a dirname per file: forking hundreds of processes on a loaded
# host delayed lint's start by over a minute.
#
# Self-test: scripts/test-lint-scope.sh.
set -euo pipefail
nested=$(git ls-files '*/go.mod' | awk -F/ '{ NF--; print }' OFS=/ | paste -sd' ' -)
awk -F/ -v nested="$nested" '
  BEGIN { OFS = "/"; n = split(nested, mods, " ") }
  NF > 1 {
    NF--
    for (i = 1; i <= n; i++) if ($0 == mods[i] || index($0, mods[i] "/") == 1) next
    print
  }' | sort -u | while read -r d; do if [ -d "$d" ]; then echo "$d"; fi; done | paste -sd, -
