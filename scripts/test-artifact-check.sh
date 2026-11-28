#!/usr/bin/env bash
# test-artifact-check.sh — refuse a downloaded test-run artifact that could make
# forge read a file outside it.
#
#   scripts/test-artifact-check.sh <runs dir>
#
# Run by test-upload-merge-queue.yml, from MAIN's checkout, between downloading
# the merge gate's merge-gate-test-runs-<attempt> artifact and uploading it. The
# merge group's own code wrote that artifact, so it is untrusted, and the job
# that uploads it holds `id-token: write`. forge (`forge test report`) joins each
# lane's capture.json `results_file` and `coverage[].path` to the lane directory
# and reads (and uploads) what it finds there. A path that escapes the lane
# (`../../../../proc/self/environ`), an absolute one, or a symlink would have it
# read and send this job's environment, OIDC request credential included.
#
# So, before forge sees anything, every entry under <runs dir> must be a regular
# file or a directory (no symlink, fifo, socket or device), every run.json and
# capture.json must parse as a JSON object, and every path a capture.json names
# must be relative, made of plain segments ([A-Za-z0-9._-], never `.` or `..`)
# and so inside its lane directory. On any refusal the runs dir is deleted, one
# ::warning (or stderr line outside Actions) says why, and the script exits 1:
# the upload step then does not run. Exit 0: the artifact is safe to hand to
# forge, or there was nothing to check. It never changes a verdict: the job is
# continue-on-error and no check reads it.
set -uo pipefail

dir=${1:-}
if [ -z "$dir" ]; then
  echo "usage: $0 <runs dir>" >&2
  exit 2
fi
[ -e "$dir" ] || exit 0

refuse() {
  rm -rf -- "$dir"
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::warning title=Test results refused::the test-run artifact was refused and nothing was uploaded: $*"
  else
    echo "test-artifact-check: refused, nothing uploaded: $*" >&2
  fi
  exit 1
}

command -v jq >/dev/null 2>&1 || refuse "jq is not installed, so the manifests cannot be checked"
if [ -L "$dir" ] || [ ! -d "$dir" ]; then refuse "${dir} is not a directory"; fi

# Anything but a regular file or a directory (find -type l, p, s, b, c, D).
bad=$(find "$dir" ! -type f ! -type d -print -quit 2>/dev/null) || refuse "cannot walk ${dir}"
[ -z "$bad" ] || refuse "${bad#"$dir"/} is not a regular file or a directory"

seg='[A-Za-z0-9_-][A-Za-z0-9._-]*'
safe_path() {
  local p=$1 s
  [[ $p =~ ^${seg}(/${seg})*/?$ ]] || return 1
  local IFS=/
  for s in $p; do
    case "$s" in . | ..) return 1 ;; esac
  done
  return 0
}

while IFS= read -r -d '' m; do
  rel=${m#"$dir"/}
  jq -e 'type == "object"' "$m" >/dev/null 2>&1 || refuse "${rel} is not a JSON object"
  [ "$(basename "$m")" = capture.json ] || continue
  paths=$(jq -r '
    [ (.results_file // empty),
      ((.coverage // []) | if type == "array" then .[] else error("coverage is not an array") end
        | if type == "object" then (.path // "") else error("a coverage entry is not an object") end) ]
    | .[] | if type == "string" then . else error("a path is not a string") end
    | @json' "$m" 2>/dev/null) || refuse "${rel} has a malformed results_file or coverage list"
  while IFS= read -r q; do
    [ -n "$q" ] || continue
    p=$(jq -r . <<<"$q")
    [ -n "$p" ] || refuse "${rel} names an empty path"
    safe_path "$p" || refuse "${rel} names a path outside its lane: $(jq -c . <<<"$q" | cut -c1-200)"
  done <<<"$paths"
done < <(find "$dir" -type f \( -name run.json -o -name capture.json \) -print0)

exit 0
