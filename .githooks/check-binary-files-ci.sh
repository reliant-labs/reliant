#!/usr/bin/env bash
#
# CI half of the committed-binary guard: rejects a compiled binary or oversized
# blob ADDED by the range under review.
#
# The pre-commit hook is the fast feedback, but it is opt-in (it only runs after
# `make setup-hooks`) and `git commit --no-verify` skips it outright. This runs
# where neither is true.
#
# Usage: check-binary-files-ci.sh <base-ref> <head-ref>
#
# Only files ADDED in the range are inspected, so binaries already in history do
# not fail unrelated PRs — removing those is a separate, deliberate operation.

set -uo pipefail

MAX_BLOB_BYTES=${MAX_BLOB_BYTES:-5242880} # 5 MiB

base="${1:?usage: check-binary-files-ci.sh <base-ref> <head-ref>}"
head="${2:?usage: check-binary-files-ci.sh <base-ref> <head-ref>}"

repo_root="$(git rev-parse --show-toplevel)"
allowlist="${repo_root}/.binary-allowlist"

is_allowlisted() {
  [ -f "${allowlist}" ] || return 1
  grep -qxF -- "$1" "${allowlist}" 2>/dev/null
}

merge_base="$(git merge-base "${base}" "${head}" 2>/dev/null)" || merge_base="${base}"

status=0
while IFS= read -r file; do
  [ -n "${file}" ] || continue
  is_allowlisted "${file}" && continue

  blob_sha="$(git rev-parse "${head}:${file}" 2>/dev/null)" || continue
  mime="$(git cat-file -p "${blob_sha}" 2>/dev/null | file -b --mime-type - 2>/dev/null)"
  size="$(git cat-file -s "${blob_sha}" 2>/dev/null || echo 0)"

  case "${mime}" in
    application/x-mach-binary|application/x-executable|application/x-sharedlib|application/x-object|application/x-archive)
      echo "::error file=${file}::Compiled binary committed (${mime}). Build with 'go run' or 'go build -o dist/', or gitignore the path."
      status=1
      continue
      ;;
  esac

  if [ "${size}" -gt "${MAX_BLOB_BYTES}" ]; then
    echo "::error file=${file}::Large file committed ($((size / 1024 / 1024)) MiB). Large blobs live in git forever; add to .binary-allowlist if intentional."
    status=1
  fi
done < <(git diff --name-only --diff-filter=A "${merge_base}" "${head}")

if [ "${status}" -eq 0 ]; then
  echo "No compiled binaries or oversized blobs added."
fi
exit "${status}"
