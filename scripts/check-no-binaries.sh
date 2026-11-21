#!/usr/bin/env bash
#
# Refuses a change that adds a binary file or a new file over the size cap.
#
# Compiled artifacts do not belong in git: they are large, they never diff
# usefully, they live in history forever, and they embed the build machine's
# absolute paths. This check used to read only STAGED files, which is nothing
# in CI (pre-commit/action runs --all-files with an empty index), and
# classified files with file(1), which a machine without it answered with
# nothing, so every file passed. control-plane#1952 merged a 5.9 MB build
# binary through exactly that gap.
#
# Two modes, one rule:
#   scripts/check-no-binaries.sh                  the staged change (pre-commit hook)
#   scripts/check-no-binaries.sh --range BASE [HEAD]
#                                                 every commit from the merge base
#                                                 of BASE and HEAD (default HEAD) to
#                                                 HEAD, each judged against its
#                                                 parent (ci.yml's pr-checks job:
#                                                 --range HEAD^1 HEAD, the pull
#                                                 request's whole history)
#
# Every commit, not the endpoints: a binary committed and deleted again later
# in the branch is absent from the net diff, yet its blob enters history for
# good when the branch merges. A merge commit is judged by its combined diff
# (the paths it differs on from every parent): what it brings in from a side
# branch is judged in that branch's own commits, which the range includes.
# A shallow clone whose history stops inside the range is exit 2.
#
# Detection needs git and nothing else:
#   - binary: git's own verdict, the `-<TAB>-` that `git diff --numstat` prints
#     for a file it will not diff as text (a NUL byte in the first 8000 bytes,
#     or a `binary` / `-diff` attribute). Every compiled executable, archive and
#     image has one. Added, modified and type-changed files are judged: turning
#     a text file or a symlink into a binary is the same defect.
#   - size: a NEW file over MAX_NEW_FILE_BYTES (default 1 MiB) fails, text or
#     not: generated bundles and data dumps are the other way history bloats.
#     A path whose type changed (a symlink replaced by a regular file) is new
#     content and is judged the same way.
# A missing tool, an unresolvable base or a git command that fails is exit 2,
# never a pass: a check that cannot run has not checked anything.
#
# A file that must be tracked anyway: add its path to .binary-allowlist at the
# repository root (one path or glob per line, * crossing /; # starts a comment).

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
  -h | --help) sed -n '2,43p' "$0"; exit 0 ;;
  *) die "unknown argument '$1' (see --help)" ;;
esac

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
# --literal-pathspecs: a merge's paths are passed as pathspecs, verbatim.
common=(--no-renames --no-ext-diff --no-textconv -z)

status=0 checked=0

# judge <label> <diff args...> [-- <paths>...]: report every binary among the
# added, modified and type-changed files of one diff, and every new or
# type-changed regular file over the cap. <label> names the change in messages.
judge() {
  local label="$1"
  shift
  git --literal-pathspecs diff "${common[@]}" --numstat --diff-filter=AMT "$@" > "${tmp}/numstat" \
    || die "git diff --numstat $* failed"
  git --literal-pathspecs diff "${common[@]}" --raw --no-abbrev --diff-filter=AT "$@" > "${tmp}/raw" \
    || die "git diff --raw $* failed"

  local rec ins rest del path meta newmode newsha size
  # numstat -z, without renames: "<added>\t<deleted>\t<path>\0".
  while IFS= read -r -d '' rec; do
    checked=$((checked + 1))
    ins="${rec%%$'\t'*}" rest="${rec#*$'\t'}"
    del="${rest%%$'\t'*}" path="${rest#*$'\t'}"
    if [ "${ins}" = - ] && [ "${del}" = - ] && ! is_allowlisted "${path}"; then
      echo "no-binaries: ${path}: binary file (git cannot diff it as text)${label}" >&2
      status=1
    fi
  done < "${tmp}/numstat"

  # raw -z: ":<oldmode> <newmode> <oldsha> <newsha> <status>\0<path>\0".
  while IFS= read -r -d '' meta && IFS= read -r -d '' path; do
    read -r _ newmode _ newsha _ <<< "${meta#:}"
    # A gitlink (submodule) names a commit of another repository, not a blob.
    [ "${newmode}" = 160000 ] && continue
    is_allowlisted "${path}" && continue
    size="$(git cat-file -s "${newsha}" 2> /dev/null)" || die "cannot read the size of ${path} (${newsha})"
    if [ "${size}" -gt "${MAX_NEW_FILE_BYTES}" ]; then
      echo "no-binaries: ${path}: new file of ${size} bytes, over the ${MAX_NEW_FILE_BYTES}-byte cap${label}" >&2
      status=1
    fi
  done < "${tmp}/raw"
}

if [ "${mode}" = range ]; then
  base_c="$(git rev-parse --verify --quiet "${base}^{commit}")" || die "cannot resolve base '${base}' to a commit"
  head_c="$(git rev-parse --verify --quiet "${head}^{commit}")" || die "cannot resolve head '${head}' to a commit"
  from="$(git merge-base "${base_c}" "${head_c}")" || die "${base} and ${head} share no history"
  where="${from:0:12}..${head_c:0:12}"
  empty_tree="$(git hash-object -t tree /dev/null)" || die "cannot name the empty tree"
  shallow_file=""
  if [ "$(git rev-parse --is-shallow-repository)" = true ]; then
    shallow_file="$(git rev-parse --git-path shallow)" || die "cannot locate the shallow file"
  fi
  git rev-list --reverse "${from}..${head_c}" > "${tmp}/commits" \
    || die "git rev-list ${from}..${head_c} failed"
  commits=0
  while read -r c; do
    commits=$((commits + 1))
    line="$(git rev-list --parents -n 1 "${c}")" || die "cannot read the parents of ${c}"
    read -r -a parents <<< "${line#"${c}"}"
    label=" (commit ${c:0:12})"
    case "${#parents[@]}" in
      0)
        # A shallow boundary commit has parents git was never given: its diff
        # against the empty tree would be the whole repository, and its real
        # change is unknown.
        if [ -n "${shallow_file}" ] && grep -qx "${c}" "${shallow_file}"; then
          die "the clone is shallow and ${c:0:12} in ${where} has no fetched parent: fetch the range's whole history (actions/checkout fetch-depth: 0)"
        fi
        judge "${label}" "${empty_tree}" "${c}"
        ;;
      1) judge "${label}" "${parents[0]}" "${c}" ;;
      *)
        git diff-tree -r -c --no-commit-id --name-only -z "${c}" > "${tmp}/merge-paths" \
          || die "git diff-tree -c ${c} failed"
        paths=()
        while IFS= read -r -d '' p; do paths+=("${p}"); done < "${tmp}/merge-paths"
        [ "${#paths[@]}" -eq 0 ] || judge "${label}" "${parents[0]}" "${c}" -- "${paths[@]}"
        ;;
    esac
  done < "${tmp}/commits"
  where="${where} (${commits} commit(s))"
else
  judge "" --cached
  where="the staged change"
fi

if [ "${status}" -ne 0 ]; then
  echo "no-binaries: in ${where}. Build artifacts belong in a gitignored directory (go build -o dist/, or go run);" >&2
  echo "             remove the file from the branch's history, or, if it truly must be tracked, add its path to .binary-allowlist." >&2
  exit 1
fi
echo "no-binaries: ${checked} added, modified or type-changed file(s) in ${where}: no binary, no new file over ${MAX_NEW_FILE_BYTES} bytes"
