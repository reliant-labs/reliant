#!/usr/bin/env bash
#
# Refuses a change that adds a binary file or a new file over the size cap.
#
# Compiled artifacts do not belong in git: they are large, they never diff
# usefully, they live in history forever, and they embed the build machine's
# absolute paths. This guard used to classify files with file(1) and pass
# whatever it could not classify, so a machine without file(1) passed every
# file; and a range with no merge base judged nothing. control-plane#1952
# merged a 5.9 MB build binary through the same gap.
#
# Two modes, one rule:
#   .githooks/check-binary-files.sh               the staged change (.githooks/pre-commit)
#   .githooks/check-binary-files.sh --range BASE [HEAD]
#                                                 every commit from the merge base
#                                                 of BASE and HEAD (default HEAD) to
#                                                 HEAD, merges included (forge.yaml's
#                                                 no-binaries gate check, and
#                                                 task ci:repo-checks)
#
# Detection needs git and nothing else:
#   - binary: git's own verdict, the `-<TAB>-` that `git diff --numstat` prints
#     for a file it will not diff as text (a NUL byte in the first 8000 bytes,
#     or a `binary` / `-diff` attribute). Every compiled executable, archive and
#     image has one. Added and modified files are judged: turning a text file
#     into a binary is the same defect.
#   - size: an ADDED file over MAX_NEW_FILE_BYTES (default 1 MiB) fails, text or
#     not: generated bundles and data dumps are the other way history bloats.
# A missing tool, an unresolvable base or a git command that fails is exit 2,
# never a pass: a check that cannot run has not checked anything.
#
# A file that must be tracked anyway: add its path to .binary-allowlist at the
# repository root (one path or glob per line, * crossing /; # starts a comment;
# it lists the media the app and its docs ship). Tested by scripts/test-binary-files.sh.

set -uo pipefail

MAX_NEW_FILE_BYTES=${MAX_NEW_FILE_BYTES:-1048576} # 1 MiB

die() {
  echo "no-binaries: $*" >&2
  echo "no-binaries: refusing to pass a check that did not run." >&2
  exit 2
}

command -v git > /dev/null 2>&1 || die "git is not on PATH: cannot read the diff"
[[ "${MAX_NEW_FILE_BYTES}" =~ ^[0-9]+$ ]] || die "MAX_NEW_FILE_BYTES=${MAX_NEW_FILE_BYTES} is not a byte count"
repo_root="$(git rev-parse --show-toplevel 2> /dev/null)" || die "not inside a git work tree ($(pwd))"
cd "${repo_root}" || die "cannot cd to ${repo_root}"

mode=staged base="" head=HEAD
case "${1:-}" in
  "") ;;
  --range)
    mode=range
    base="${2:-}"
    head="${3:-HEAD}"
    [ -n "${base}" ] || die "--range needs a base revision"
    ;;
  -h | --help) sed -n '2,34p' "$0"; exit 0 ;;
  *) die "unknown argument '$1' (see --help)" ;;
esac

# diff_args: what `git diff` compares. blob_rev <path>: where that path's new
# content is, for its size.
if [ "${mode}" = range ]; then
  base_c="$(git rev-parse --verify --quiet "${base}^{commit}")" || die "cannot resolve base '${base}' to a commit"
  head_c="$(git rev-parse --verify --quiet "${head}^{commit}")" || die "cannot resolve head '${head}' to a commit"
  from="$(git merge-base "${base_c}" "${head_c}")" || die "${base} and ${head} share no history"
  diff_args=("${from}" "${head_c}")
  blob_rev() { printf '%s:%s' "${head_c}" "$1"; }
  where="${from:0:12}..${head_c:0:12}"
else
  diff_args=(--cached)
  blob_rev() { printf ':%s' "$1"; }
  where="the staged change"
fi

allow=()
if [ -f .binary-allowlist ]; then
  while IFS= read -r line || [ -n "${line}" ]; do
    line="${line%%#*}"
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [ -z "${line}" ] || allow+=("${line}")
  done < .binary-allowlist
fi
# is_allowlisted <path>: a line of .binary-allowlist names it: an exact path,
# or a glob in which * also crosses / (docs/images/*.png).
is_allowlisted() {
  local pat
  for pat in ${allow[@]+"${allow[@]}"}; do
    # shellcheck disable=SC2053 # the pattern is a glob on purpose
    [[ "$1" == ${pat} ]] && return 0
  done
  return 1
}

tmp="$(mktemp -d)" || die "mktemp failed"
trap 'rm -rf "${tmp}"' EXIT
# --no-renames: a rename is judged as the add it is. --no-textconv and
# --no-ext-diff: git's own binary verdict, not a configured converter's.
common=(--no-renames --no-ext-diff --no-textconv -z)
git diff "${common[@]}" --numstat --diff-filter=AM "${diff_args[@]}" -- > "${tmp}/numstat" \
  || die "git diff --numstat ${diff_args[*]} failed"
git diff "${common[@]}" --name-only --diff-filter=A "${diff_args[@]}" -- > "${tmp}/added" \
  || die "git diff --name-only ${diff_args[*]} failed"

status=0 checked=0

# numstat -z, without renames: "<added>\t<deleted>\t<path>\0".
while IFS= read -r -d '' rec; do
  checked=$((checked + 1))
  ins="${rec%%$'\t'*}" rest="${rec#*$'\t'}"
  del="${rest%%$'\t'*}" path="${rest#*$'\t'}"
  if [ "${ins}" = - ] && [ "${del}" = - ] && ! is_allowlisted "${path}"; then
    echo "no-binaries: ${path}: binary file (git cannot diff it as text)" >&2
    status=1
  fi
done < "${tmp}/numstat"

while IFS= read -r -d '' path; do
  is_allowlisted "${path}" && continue
  size="$(git cat-file -s "$(blob_rev "${path}")" 2> /dev/null)" || die "cannot read the size of ${path} ($(blob_rev "${path}"))"
  if [ "${size}" -gt "${MAX_NEW_FILE_BYTES}" ]; then
    echo "no-binaries: ${path}: new file of ${size} bytes, over the ${MAX_NEW_FILE_BYTES}-byte cap" >&2
    status=1
  fi
done < "${tmp}/added"

if [ "${status}" -ne 0 ]; then
  echo "no-binaries: in ${where}. Build artifacts belong in a gitignored directory (go build -o dist/, or go run);" >&2
  echo "             remove the file from the branch's history, or, if it truly must be tracked, add its path to .binary-allowlist." >&2
  exit 1
fi
echo "no-binaries: ${checked} added or modified file(s) in ${where}: no binary, no new file over ${MAX_NEW_FILE_BYTES} bytes"
