#!/usr/bin/env bash
#
# Blocks a commit whose staged additions introduce a locally-forbidden string.
#
# Forbidden patterns are read from a gitignored ".forbidden-strings" file at the
# repository root: one literal string per line; blank lines and lines beginning
# with "#" are ignored. If that file does not exist this check is a no-op. Keeping
# the patterns in a gitignored file ensures the forbidden values are never
# themselves committed by this guard.
#
# Only *added* lines of added/copied/modified files are inspected, so it does not
# fire on pre-existing occurrences you are not touching.

set -uo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || exit 0
patterns_file="${repo_root}/.forbidden-strings"
[ -f "${patterns_file}" ] || exit 0

status=0
while IFS= read -r pattern || [ -n "${pattern}" ]; do
  case "${pattern}" in
    ''|'#'*) continue ;;
  esac
  while IFS= read -r file; do
    [ -n "${file}" ] || continue
    if git diff --cached -U0 --diff-filter=ACM -- "${file}" \
        | grep '^+' | grep -v '^+++' | grep -Fq -- "${pattern}"; then
      echo "pre-commit: forbidden string in staged additions of ${file}: ${pattern}" >&2
      status=1
    fi
  done < <(git diff --cached --name-only --diff-filter=ACM)
done < "${patterns_file}"

if [ "${status}" -ne 0 ]; then
  echo "pre-commit: commit blocked. Remove the flagged additions before committing." >&2
fi
exit "${status}"
