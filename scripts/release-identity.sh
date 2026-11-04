#!/usr/bin/env bash
# release-identity.sh — THE answer to "which reliant is this build?"
#
# Every artifact that reports a version derives it here: the web bundle (Vite,
# frontends/web/vite-plugin-version-json.ts, which also writes /version.json), the Go
# binary's internal/version.Version (Taskfile.yml, release.yml, build-images.yml,
# and control-plane's scripts/build-reliant-image.sh, which builds the prod
# reliant image). One resolver, so the About page's "app" and "server" lines
# can only disagree when the two really are different builds.
#
# Output (stdout), no leading "v" — the same shape release.yml has always
# stamped and electron's app.getVersion() reports:
#
#   1.8.1                  built AT a release tag
#   1.8.2-dev+8bcfc6ee     built at a commit after release 1.8.1: the next
#                          patch's dev build, plus the commit (semver build
#                          metadata). A commit pin — control-plane's prod mode
#                          today — lands here.
#   8bcfc6ee               a commit, with no release base to place it after
#
# A trailing ".dirty" marks a build of a working tree with uncommitted changes.
#
# Exits 1, printing nothing on stdout, when it can name neither a tag nor a
# commit. Callers that ship treat that as fatal: the bug this exists to end is
# prod's About page reading "version unknown" because nothing stamped one.
#
# WHY NOT `git describe`. It needs tags and the history between the tag and the
# commit. Neither survives the clones that actually build releases: forge
# fetches a GitSource with --depth 1 and no tags, and actions/checkout defaults
# to the same. So describe is used ONLY to recognise an exact tag; the release a
# commit follows comes from electron/package.json — the version scripts/release.sh
# bumps, present in every checkout and every `git archive` export.
#
# Usage: release-identity.sh [--commit-time] [--repo DIR] [--commit SHA] [--tag TAG] [--ref REF]
#   --commit-time print the build number instead of the identity: the committer
#                 time (unix seconds) of the commit being built. It is the key
#                 that orders builds — semver cannot, since every build between
#                 two tags carries the same X.Y.Z (and ignores the +sha). Prints
#                 0 ("unordered": never forced, shown as a dev build) when the
#                 commit cannot be read or the tree is dirty; never fails.
#   --repo DIR    the git checkout to read (default: the one holding this script)
#   --commit SHA  the commit being built (default: HEAD of --repo). Naming it
#                 skips the dirty check: the caller is describing a commit, not
#                 a working tree.
#   --tag TAG     the release tag being built, when the caller knows it (a
#                 tag-triggered CI run). Wins over everything.
#   --ref REF     a pin that MAY be a tag — forge's FORGE_SOURCE_REF. Taken as
#                 the tag only when it is a version (v1.8.1), never a branch or sha.
set -euo pipefail

repo=""
commit=""
tag=""
ref=""
want_commit_time=false
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="${2:-}"; shift 2 ;;
    --commit) commit="${2:-}"; shift 2 ;;
    --tag) tag="${2:-}"; shift 2 ;;
    --ref) ref="${2:-}"; shift 2 ;;
    --commit-time) want_commit_time=true; shift ;;
    -h|--help) sed -n '2,50p' "$0"; exit 0 ;;
    *) echo "release-identity.sh: unknown argument '$1' (see --help)" >&2; exit 2 ;;
  esac
done

if [ -z "$repo" ]; then
  repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi

# A release version: v?X.Y.Z with an optional prerelease (1.8.2-rc1, 1.8.2-rc.1).
version_re='^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

git_ok=false
if command -v git >/dev/null 2>&1 && git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then
  git_ok=true
fi

# ── 1. a tag, if this build is one ─────────────────────────────────────────
if [ -z "$tag" ] && [[ "$ref" =~ $version_re ]]; then
  tag="$ref"
fi
explicit_commit="$commit"
if [ -z "$commit" ] && $git_ok; then
  commit="$(git -C "$repo" rev-parse HEAD 2>/dev/null || true)"
elif [ -n "$commit" ] && $git_ok; then
  # Normalise an abbreviated sha to the full one when the repo has it.
  commit="$(git -C "$repo" rev-parse --verify --quiet "${commit}^{commit}" 2>/dev/null || echo "$commit")"
fi
if $want_commit_time; then
  # The committer time of a commit, not of the build: two builds of one commit
  # carry one number. A working tree with uncommitted changes is not a commit.
  ct=0
  if [ -n "$commit" ] && $git_ok; then
    if [ -n "$explicit_commit" ] || git -C "$repo" diff --quiet HEAD -- 2>/dev/null; then
      ct="$(git -C "$repo" show -s --format=%ct "${commit}^{commit}" 2>/dev/null || echo 0)"
    fi
  fi
  [[ "$ct" =~ ^[0-9]+$ ]] || ct=0
  echo "$ct"
  exit 0
fi
if [ -z "$tag" ] && [ -n "$commit" ] && $git_ok; then
  tag="$(git -C "$repo" describe --tags --exact-match --match 'v[0-9]*' "$commit" 2>/dev/null || true)"
fi
if [ -n "$tag" ]; then
  if ! [[ "$tag" =~ $version_re ]]; then
    echo "release-identity.sh: tag '$tag' is not a release version (vX.Y.Z[-pre])" >&2
    exit 1
  fi
  echo "${tag#v}"
  exit 0
fi

# ── 2. otherwise a commit, placed after the release it follows ──────────────
if ! [[ "$commit" =~ ^[0-9a-fA-F]{7,40}$ ]]; then
  echo "release-identity.sh: cannot name this build — no release tag and no commit (repo: $repo)" >&2
  exit 1
fi
short="$(printf '%s' "$commit" | cut -c1-8 | tr 'A-F' 'a-f')"

dirty=""
if [ -z "$explicit_commit" ] && $git_ok && ! git -C "$repo" diff --quiet HEAD -- 2>/dev/null; then
  dirty=".dirty"
fi

# The release this commit follows: electron/package.json AT that commit (the
# repo's checkout may be elsewhere), else the file on disk.
pkg=""
if $git_ok; then
  pkg="$(git -C "$repo" show "${commit}:electron/package.json" 2>/dev/null || true)"
fi
if [ -z "$pkg" ] && [ -z "$explicit_commit" ] && [ -f "$repo/electron/package.json" ]; then
  pkg="$(cat "$repo/electron/package.json")"
fi
base="$(printf '%s\n' "$pkg" | sed -n 's/^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"

if [[ "$base" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)(-.*)?$ ]]; then
  major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}" patch="${BASH_REMATCH[3]}" pre="${BASH_REMATCH[4]}"
  # After a release X.Y.Z the next one is X.Y.(Z+1); after a prerelease of
  # X.Y.Z (1.8.2-rc1) the next one is still X.Y.Z.
  if [ -z "$pre" ]; then
    patch=$((patch + 1))
  fi
  echo "${major}.${minor}.${patch}-dev+${short}${dirty}"
  exit 0
fi

echo "${short}${dirty}"
