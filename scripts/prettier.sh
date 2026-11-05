#!/usr/bin/env bash
# prettier.sh — this repo's prettier: the version the pre-commit hook pins,
# over the files the hook formats.
#
#   scripts/prettier.sh [--check] [FILE...]   (task fmt:web -- [FILE...])
#   scripts/prettier.sh --version-pin
#
# Never run a bare `npx prettier`. It resolves whatever release is newest (or
# cached) on your machine, not the hook's, and two prettier releases disagree
# on lines nobody touched: a local 3.6 reformatted untouched files and failed
# the Checks job, which runs the hook at the pin.
#
# ONE source of truth: the prettier hook in .pre-commit-config.yaml. This
# script reads its pin (additional_dependencies: ["prettier@X.Y.Z"]) and its
# `files` and `exclude` patterns from there, so the hook, this script and the
# release's CHANGELOG formatting (scripts/release-forge.sh, through
# --version-pin) cannot drift apart. TestPrettierVersionIsPinnedEverywhere holds
# that pin equal to templates.PrettierVersion, the prettier scaffolded frontends
# run, and TestPrettierScriptFormatsWhatTheHookFormats holds this script to the
# hook's file selection.
#
# FILEs are repo-relative (or absolute paths inside the repo). With none, it
# formats the files you changed: the diff from the merge-base with origin/main,
# plus untracked files — never the rest of the tree. Files the hook would not
# format (wrong extension, or excluded: CHANGELOG.md, testdata/, skills, gen/,
# vendored CRDs) are dropped, as the hook drops them. --check reports instead of
# writing.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cfg="$root/.pre-commit-config.yaml"

# hook_field KEY prints KEY's value inside the `- id: prettier` hook.
hook_field() {
  awk -v key="$1" '
    /^[[:space:]]*- id:[[:space:]]*/ { in_hook = ($0 ~ /- id:[[:space:]]*prettier[[:space:]]*$/) ; next }
    in_hook && $0 ~ "^[[:space:]]+" key ":[[:space:]]" {
      sub("^[[:space:]]+" key ":[[:space:]]+", ""); print; exit
    }
  ' "$cfg"
}

pin="$(hook_field additional_dependencies | sed -n 's/.*"prettier@\([0-9][0-9.]*\)".*/\1/p')"
if [ -z "$pin" ]; then
  echo "error: no prettier@X.Y.Z pin in the prettier hook of $cfg" >&2
  exit 1
fi

mode=--write
case "${1:-}" in
  --version-pin)
    echo "$pin"
    exit 0
    ;;
  --check)
    mode=--check
    shift
    ;;
esac

files_re="$(hook_field files)"
exclude_re="$(hook_field exclude)"
if [ -z "$files_re" ] || [ -z "$exclude_re" ]; then
  echo "error: the prettier hook in $cfg has no files/exclude pattern to select files with" >&2
  exit 1
fi

cd "$root"
candidates=()
if [ "$#" -gt 0 ]; then
  for f in "$@"; do
    candidates+=("${f#"$root"/}")
  done
else
  base="$(git merge-base HEAD origin/main 2>/dev/null)" || {
    echo "error: no merge-base with origin/main to find your changes from; name the files" >&2
    exit 1
  }
  while IFS= read -r f; do
    candidates+=("$f")
  done < <({ git diff --name-only --diff-filter=d "$base"; git ls-files --others --exclude-standard; } | sort -u)
fi

selected=()
if [ "${#candidates[@]}" -gt 0 ]; then
  while IFS= read -r f; do
    selected+=("$f")
  done < <(printf '%s\n' "${candidates[@]}" | grep -E -- "$files_re" | grep -Ev -- "$exclude_re" || true)
fi
if [ "${#selected[@]}" -eq 0 ]; then
  echo "prettier@$pin: nothing to format (no changed file the pre-commit hook formats)"
  exit 0
fi
exec npx --yes "prettier@$pin" "$mode" --ignore-unknown "${selected[@]}"
