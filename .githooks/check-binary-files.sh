#!/usr/bin/env bash
#
# Blocks a commit that adds a compiled binary or an oversized blob.
#
# Compiled artifacts do not belong in git: they are large, they never diff
# usefully, and they embed the build machine's absolute paths (a Go binary
# built in a home directory carries that path hundreds of times over). The
# docgen binaries this guard was written for held ~470 such references.
#
# Detection is by CONTENT, not filename — an executable committed under a name
# nobody thought to gitignore is exactly the case that slips through. Only
# ADDED files are inspected, so a binary already in history does not block
# unrelated work; removing that history is a separate, deliberate operation.
#
# Both limits can be tuned; MAX_BLOB_BYTES is deliberately generous so it fires
# on build output rather than on a legitimately chunky fixture.
#
# Escape hatch, for the rare file that must be tracked:
#   git commit --no-verify        (skips every hook)
# or add the path to .binary-allowlist, one path per line.

set -uo pipefail

MAX_BLOB_BYTES=${MAX_BLOB_BYTES:-5242880} # 5 MiB

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || exit 0
allowlist="${repo_root}/.binary-allowlist"

is_allowlisted() {
  [ -f "${allowlist}" ] || return 1
  grep -qxF -- "$1" "${allowlist}" 2>/dev/null
}

status=0

# --diff-filter=A: only newly ADDED files. A binary already tracked stays put.
while IFS= read -r file; do
  [ -n "${file}" ] || continue
  is_allowlisted "${file}" && continue

  # Read the staged blob, not the working tree: they can differ, and the blob
  # is what actually lands in the commit.
  blob="$(git show ":${file}" 2>/dev/null)" || continue

  mime="$(git show ":${file}" 2>/dev/null | file -b --mime-type - 2>/dev/null)"
  size="$(git cat-file -s "$(git rev-parse ":${file}" 2>/dev/null)" 2>/dev/null || echo 0)"

  case "${mime}" in
    application/x-mach-binary|application/x-executable|application/x-sharedlib|application/x-object|application/x-archive)
      echo "pre-commit: refusing to add a compiled binary: ${file} (${mime})" >&2
      echo "            Build it with 'go run' / 'go build -o dist/' instead, or gitignore the path." >&2
      status=1
      continue
      ;;
  esac

  if [ "${size}" -gt "${MAX_BLOB_BYTES}" ]; then
    echo "pre-commit: refusing to add a large file: ${file} ($((size / 1024 / 1024)) MiB > $((MAX_BLOB_BYTES / 1024 / 1024)) MiB)" >&2
    echo "            Large blobs live in git forever. If this is intentional, add it to .binary-allowlist." >&2
    status=1
  fi
done < <(git diff --cached --name-only --diff-filter=A)

if [ "${status}" -ne 0 ]; then
  echo "pre-commit: commit blocked. Use 'git commit --no-verify' only if you are certain." >&2
fi
exit "${status}"
