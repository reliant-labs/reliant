#!/usr/bin/env bash
# semver-next.sh: the next release version, computed from the conventional-commit
# subjects (squash-merged PR titles) since the last final release.
#
# Owner decision, 2026-11-05: real semver. Majors are locked at 1; a release with
# any `feat` is a minor bump (vX.Y+1.0), a fix-only release a patch (vX.Y.Z+1), and
# a breaking change is refused: deprecate, keep the old path, and remove it only in
# a future major, which only the owner cuts (the release script's explicit-version
# override). The same algorithm lives in control-plane's and reliant's scripts/semver-next.sh.
#
# Usage:
#   scripts/semver-next.sh [--repo DIR] [--base vX.Y.Z] [--base-rev REV] [--head REV]
#                          [--breaking-since REV] [--allow-breaking] [--explain]
#
#   --repo DIR      the git checkout to read (default: the current directory)
#   --base vX.Y.Z   the last final release (default: the newest local vX.Y.Z tag; an
#                   rc/ or pre-release tag is never a base). Not "the newest the head
#                   descends from": reliant's and control-plane's final tags sit on the
#                   release train's pin commits, beside main, never on it.
#   --base-rev REV  the commit to count from (default: the --base tag)
#   --head REV      the release candidate (default: HEAD)
#   --breaking-since REV
#                   only a breaking commit NOT reachable from REV refuses: one an earlier
#                   release candidate already carried was refused or overridden then
#                   (the release train passes its previous set's commit). It still bumps.
#   --allow-breaking
#                   print the version anyway, warning about each breaking commit (a
#                   release script checking a version the train or the owner chose)
#   --no-older-check
#                   do not refuse a head a later release descends from (the release
#                   train: a repo unchanged since its last release still gets a pin commit)
#   --explain       print each commit's bump on stderr
#
# Classifies each commit subject `type(scope)!: title`:
#   `!`, or a body line `BREAKING CHANGE:` / `BREAKING-CHANGE:`, or forge's
#   apigate declaration `API-BREAK:`                             -> breaking
#   type `feat`                                                  -> minor
#   anything else (fix, perf, chore, docs, refactor, ...)        -> patch
#
# Prints the version on stdout and exits 0; exits 3 naming every breaking commit
# (its subject carries the PR number), 4 when a later commit is already released (a
# final tag on a descendant of the head) or the computed version is already a tag, 2 on a
# usage or git error.
set -euo pipefail

REPO=. BASE="" BASE_REV="" HEAD_REV=HEAD EXPLAIN=0 SINCE="" ALLOW=0 OLDER=1
die() { echo "semver-next: $*" >&2; exit 2; }
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) REPO="${2:?}"; shift 2 ;;
    --base) BASE="${2:?}"; shift 2 ;;
    --base-rev) BASE_REV="${2:?}"; shift 2 ;;
    --head) HEAD_REV="${2:?}"; shift 2 ;;
    --breaking-since) SINCE="${2:?}"; shift 2 ;;
    --allow-breaking) ALLOW=1; shift ;;
    --no-older-check) OLDER=0; shift ;;
    --explain) EXPLAIN=1; shift ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument $1" ;;
  esac
done

git -C "$REPO" rev-parse -q --verify "$HEAD_REV^{commit}" >/dev/null || die "cannot resolve $HEAD_REV in $REPO"
finals() { grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V || true; }
# A final release on a later commit (a descendant of the head) already shipped
# newer source: any version computed here would publish older source as a newer
# release. Refuse, naming it.
released="$(comm -23 <(git -C "$REPO" tag -l 'v*' --contains "$HEAD_REV" | sort) \
  <(git -C "$REPO" tag -l 'v*' --points-at "$HEAD_REV" | sort) | finals | tail -1)"
if [ -n "$released" ] && [ "$OLDER" = 1 ]; then
  echo "semver-next: refusing: $HEAD_REV is older than a release: $released is on a later commit; a release of it would be older source under a newer version" >&2
  exit 4
fi
if [ -z "$BASE" ]; then
  BASE="$(git -C "$REPO" tag -l 'v*' | finals | tail -1)"
  [ -n "$BASE" ] || die "no final vX.Y.Z tag in $REPO to count from (pass --base)"
fi
[[ "$BASE" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || die "--base $BASE is not vX.Y.Z"
major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}" patch="${BASH_REMATCH[3]}"
[ -n "$BASE_REV" ] || BASE_REV="refs/tags/$BASE"
git -C "$REPO" rev-parse -q --verify "$BASE_REV^{commit}" >/dev/null || die "cannot resolve $BASE_REV in $REPO"
[ -z "$SINCE" ] || git -C "$REPO" rev-parse -q --verify "$SINCE^{commit}" >/dev/null || die "cannot resolve $SINCE in $REPO"

bump=patch breaking=()
# One record per commit: sha, subject, body, NUL-terminated. Captured through a
# checked command first: a git log failing inside a process substitution would
# escape set -e and read as "no commits", a silent patch.
LOG="$(mktemp)"; trap 'rm -f "$LOG"' EXIT
git -C "$REPO" log --no-merges --format='%H%x09%s%n%b%x00' "$BASE_REV..$HEAD_REV" >"$LOG" \
  || die "git log $BASE_REV..$HEAD_REV failed in $REPO"
while IFS= read -r -d '' rec; do
  rec="${rec#$'\n'}"
  sha="${rec%%$'\t'*}"; rest="${rec#*$'\t'}"
  subject="${rest%%$'\n'*}"; body=""; [ "$rest" = "$subject" ] || body="${rest#*$'\n'}"
  kind=patch
  if [[ "$subject" =~ ^([a-z]+)(\([^\)]*\))?(!)?:\  ]]; then
    [ "${BASH_REMATCH[1]}" = feat ] && kind=minor
    [ -n "${BASH_REMATCH[3]}" ] && kind=breaking
  fi
  grep -qE '^(BREAKING[ -]CHANGE|API-BREAK):' <<<"$body" && kind=breaking
  [ "$EXPLAIN" = 1 ] && printf '%-8s %s %s\n' "$kind" "${sha:0:12}" "$subject" >&2
  case "$kind" in
    breaking)
      bump=minor
      if [ -n "$SINCE" ] && git -C "$REPO" merge-base --is-ancestor "$sha" "$SINCE"; then
        echo "semver-next: note: ${sha:0:12} $subject is breaking, but an earlier release candidate already carried it" >&2
      else breaking+=("${sha:0:12} $subject"); fi ;;
    minor) bump=minor ;;
  esac
done <"$LOG"

if [ "${#breaking[@]}" -gt 0 ] && [ "$ALLOW" = 1 ]; then
  printf 'semver-next: warning: breaking, allowed by the caller: %s\n' "${breaking[@]}" >&2
elif [ "${#breaking[@]}" -gt 0 ]; then
  {
    echo "semver-next: refusing: $(basename "$(git -C "$REPO" rev-parse --show-toplevel)") has a breaking change since $BASE, and majors are locked at $major:"
    printf '  %s\n' "${breaking[@]}"
    echo "  Make it a deprecation instead (keep the old path, mark it deprecated, remove it only"
    echo "  in a future major), or the owner cuts an explicit version with the release override."
  } >&2
  exit 3
fi
case "$bump" in
  minor) next="v$major.$((minor + 1)).0" ;;
  *) next="v$major.$minor.$((patch + 1))" ;;
esac
# The version is already a release of other source (a tag on another branch).
if git -C "$REPO" rev-parse -q --verify "refs/tags/$next" >/dev/null; then
  echo "semver-next: refusing: $next is already released, from a commit $HEAD_REV does not descend from" >&2
  exit 4
fi
echo "$next"
