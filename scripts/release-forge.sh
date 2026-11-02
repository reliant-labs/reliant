#!/usr/bin/env bash
# release-forge.sh — cut a forge release in ONE command.
#
# forge is ONE Go module, github.com/reliant-labs/forge, tagged vX.Y.Z. That
# is a recent simplification and it is most of why this script is now short.
#
# WHAT USED TO BE HERE. forge shipped a second module, forge/pkg, tagged
# pkg/vX.Y.Z, and the root module REQUIRED it. Releasing therefore meant
# tagging pkg, bumping the require, and tagging root — and that require could
# not resolve until the pkg tag was pushed, so this script carried three
# elaborate mechanisms to close the circle:
#
#   1. a two-tag atomic push, so a pkg tag could never ship without its root
#      release (or vice versa);
#   2. the go.sum trap — an in-repo `go build ./...` passed WITHOUT forge/pkg
#      hashes in go.sum because go.work resolved pkg from disk, so the script
#      made a temporary bare clone, tagged it locally, and resolved the
#      not-yet-public version through GOPROXY=direct to record real hashes;
#   3. a bump of `defaultPublishedForgePkgVersion`, the constant a dev build
#      wrote into scaffolds as the forge/pkg pin.
#
# All three are gone with the submodule. A single module cannot require
# itself, so there is no unpushed version to resolve, no second tag to order,
# and no hand-maintained pin to drift. What remains is what a release
# genuinely needs: validate, sync the version files, tag once, push.
#
# A RELEASE NEVER PUSHES TO THE BRANCH. The tag goes on a commit that is
# already on origin/<branch> and whose full CI is green; the bookkeeping
# (VERSION, NEXT_VERSION, CHANGELOG.md, npm/package.json) is committed on top
# of that commit, pushed to release/vX.Y.Z, and lands through a pull request
# and the merge queue like any other change. main is protected by a ruleset
# that only the merge queue may satisfy, so a direct push would need a bypass
# actor; this script is why none is needed. The tagged tree therefore still
# names the previous version in VERSION: the release binaries are stamped
# from the tag (scripts/build-release-assets.sh), not from that file.
#
# Usage:
#   scripts/release-forge.sh [--dry-run] [--repo <dir>] [--branch <name>]
#                            [--commit <sha>] [--skip-proxy-check] [vX.Y.Z]
#
#   vX.Y.Z        the release to cut. Defaults to NEXT_VERSION's (read at the
#                 commit being released), and must be it or a pre-release of
#                 it (step 2b); a final release moves NEXT_VERSION to its
#                 next patch.
#   --dry-run     run every validation and every file edit, print the plan,
#                 and create no commit, tag, push or pull request.
#   --repo DIR    operate on DIR instead of the enclosing git repo (tests).
#   --branch NAME the branch whose commits are released, and the base of the
#                 bookkeeping pull request (default: main). Never pushed to.
#   --commit SHA  tag SHA, a commit already on origin/<branch> whose full CI
#                 is green. Default: the newest such commit on the branch's
#                 first-parent history (step 2a), so main keeps merging while
#                 a release is cut and the tag stays reachable from it.
#   --skip-proxy-check
#                 skip the immutable-version gate (step 5). Only for an
#                 offline release where you have verified BY HAND that the
#                 version has never been published. See that step's comment.
#
# BINARIES. The GitHub release carries prebuilt archives for linux, darwin and
# windows on amd64 and arm64, plus checksums.txt (scripts/build-release-assets.sh).
# install.sh, install.ps1, the npm shim (npm/) and the Homebrew formula all
# download those; a release without them leaves every installer failing.
# npm/package.json is synced to the release version too; the npm package is
# published by .github/workflows/release-npm.yml, which the GitHub release
# created here triggers (docs/releasing.md, "Installers").
#
# CHANGELOG.md is written HERE and nowhere else. This script builds the new
# `## [vX.Y.Z] - <date>` section from the squash commits (the merged PR titles)
# since the previous tag, folds the existing `## [Unreleased]` prose in once,
# leaves `[Unreleased]` empty, formats the file with the prettier the pre-commit
# hook pins, commits it with the release, and — after the push — publishes the
# section as the GitHub release notes. Pull requests never edit the file
# (scripts/check-changelog-untouched.sh). See scripts/changelog-release.sh.
#
# Environment (tests and offline runs):
#   FORGE_RELEASE_PRETTIER   command to format CHANGELOG.md with, given the
#                            file as its argument (default: npx prettier@<pin>)
#   FORGE_RELEASE_GH         the gh binary (default: gh)
#   FORGE_RELEASE_TARGETS    GOOS/GOARCH list to build release archives for
#                            (default: the six scripts/build-release-assets.sh ships)
#   FORGE_RELEASE_GREEN_DEPTH how many first-parent commits of origin/<branch>
#                            to search for the newest green one (default: 40)
#
# See docs/releasing.md.
set -euo pipefail
RELEASE_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/release-lock.sh
. "$RELEASE_SCRIPT_DIR/release-lock.sh"

DRY_RUN=0
REPO_ROOT=""
BRANCH="main"
VERSION=""
SKIP_PROXY_CHECK=0
# Overridable so the tests can point the check at a local fixture server
# instead of the public proxy; nothing else should set it.
PROXY_BASE="${FORGE_RELEASE_PROXY_BASE:-https://proxy.golang.org}"

COMMIT=""

usage() {
  echo "usage: $0 [--dry-run] [--repo <dir>] [--branch <name>] [--commit <sha>] [--skip-proxy-check] [vX.Y.Z]" >&2
  echo "       the version defaults to NEXT_VERSION's, and must be it (or a pre-release of it)" >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --skip-proxy-check) SKIP_PROXY_CHECK=1; shift ;;
    --repo)    [ $# -ge 2 ] || usage; REPO_ROOT="$2"; shift 2 ;;
    --branch)  [ $# -ge 2 ] || usage; BRANCH="$2"; shift 2 ;;
    --commit)  [ $# -ge 2 ] || usage; COMMIT="$2"; shift 2 ;;
    -h|--help) usage ;;
    -*)        echo "error: unknown flag $1" >&2; usage ;;
    *)
      [ -z "$VERSION" ] || usage
      VERSION="$1"; shift ;;
  esac
done

# ── 1. Version shape ────────────────────────────────────────────────
# Canonical semver with optional prerelease. Reject the tag-prefixed forms
# early — maintainers habitually paste `pkg/v1.2.3` or `v1.2.3+meta` back in.
# No version at all means NEXT_VERSION's (step 2b).
if [ -n "$VERSION" ] && ! echo "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "error: version must look like vX.Y.Z (got: $VERSION)" >&2
  echo "hint: pass the bare version, with the leading v and no build metadata." >&2
  exit 1
fi
RELEASE_TAG="$VERSION"

# ── 2. Repo / module layout ─────────────────────────────────────────
if [ -z "$REPO_ROOT" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel)"
fi
cd "$REPO_ROOT"

# The GitHub repository behind origin, if it is one: it is asked for the full
# CI verdict of a commit (step 2a, step 6) and receives the bookkeeping pull
# request and the GitHub release. Local fixture remotes have no GitHub CI.
RELEASE_ORIGIN="$(git config --get remote.origin.url 2>/dev/null || true)"
RELEASE_REPO=""
case "$RELEASE_ORIGIN" in
  git@github.com:*) RELEASE_REPO="${RELEASE_ORIGIN#git@github.com:}" ;;
  https://github.com/*) RELEASE_REPO="${RELEASE_ORIGIN#https://github.com/}" ;;
  ssh://git@github.com/*) RELEASE_REPO="${RELEASE_ORIGIN#ssh://git@github.com/}" ;;
esac
RELEASE_REPO="${RELEASE_REPO%.git}"

# ── 2a. The commit to release: green, and already on origin/<branch> ──
# Never the local checkout's HEAD and never a commit this run makes: the tag
# names a commit the merge queue already landed, so nothing here has to push
# to the branch. Without --commit, the newest commit of the branch's
# first-parent history whose full CI is green (check-release-tests.sh, the
# same proof step 6 requires) is released; a local remote has no CI, so its
# tip is.
if ! git fetch -q origin "+refs/heads/$BRANCH:refs/remotes/origin/$BRANCH"; then
  echo "error: cannot fetch $BRANCH from origin; the release names a commit on origin/$BRANCH" >&2
  exit 1
fi
if [ -n "$COMMIT" ]; then
  COMMIT="$(git rev-parse --verify "$COMMIT^{commit}")"
  if ! git merge-base --is-ancestor "$COMMIT" "origin/$BRANCH"; then
    echo "error: --commit $COMMIT is not on origin/$BRANCH" >&2
    exit 1
  fi
elif [ -z "$RELEASE_REPO" ]; then
  COMMIT="$(git rev-parse "origin/$BRANCH")"
  echo "→ local/non-GitHub remote: no hosted CI to ask, so releasing origin/$BRANCH's tip $(git rev-parse --short "$COMMIT")"
else
  GREEN_DEPTH="${FORGE_RELEASE_GREEN_DEPTH:-40}"
  echo "→ finding the newest commit of origin/$BRANCH with green full tests (searching $GREEN_DEPTH)"
  for candidate in $(git rev-list --first-parent --max-count="$GREEN_DEPTH" "origin/$BRANCH"); do
    if bash "$RELEASE_SCRIPT_DIR/check-release-tests.sh" "$RELEASE_REPO" "$candidate" >/dev/null 2>&1; then
      COMMIT="$candidate"
      break
    fi
  done
  if [ -z "$COMMIT" ]; then
    echo "error: none of the newest $GREEN_DEPTH commits of origin/$BRANCH has green full tests, so there is nothing to release" >&2
    echo "hint: wait for main's CI (scripts/check-release-tests.sh $RELEASE_REPO <sha> says why a commit is not green)," >&2
    echo "      or pass --commit <sha> for an older green commit." >&2
    exit 1
  fi
  echo "  newest green: $(git log -1 --format='%h %s' "$COMMIT")"
fi

# A private detached worktree at that commit, never `git checkout` here: this
# script is being read by bash as it runs, and checking out another commit
# would swap the file under it mid-run. The caller's checkout is never edited,
# so other agents' work in it cannot be swept into a release; the checkout
# lock below still lives in the caller's git dir, so two runs from one
# checkout (which share its refs/tags/) take turns.
CALLER_ROOT="$REPO_ROOT"
CHECKOUT_LOCK="$(git rev-parse --absolute-git-dir)/release-forge.lock"
RELEASE_WORKTREE_PARENT="$(mktemp -d)"
REPO_ROOT="$RELEASE_WORKTREE_PARENT/forge-$COMMIT"
git worktree add -q --detach "$REPO_ROOT" "$COMMIT"
remove_release_worktree() {
  cd "$CALLER_ROOT" 2>/dev/null || cd /
  git -C "$CALLER_ROOT" worktree remove --force "$REPO_ROOT" >/dev/null 2>&1 || true
  rm -rf "$RELEASE_WORKTREE_PARENT"
}
# Replaced by restore_tree (step 7), which ends by calling this.
trap remove_release_worktree EXIT
cd "$REPO_ROOT"
echo "→ releasing $COMMIT in $REPO_ROOT (origin/$BRANCH is $(git rev-parse --short "origin/$BRANCH"))"


if [ ! -f go.mod ]; then
  echo "error: $REPO_ROOT/go.mod not found — not a forge repo checkout?" >&2
  exit 1
fi

# Derive the module path rather than hardcoding it. forge is ONE module: pkg/
# is a directory inside it, so there is no submodule relationship to assert
# and no directory-prefixed tag to derive.
ROOT_MODULE="$(awk '/^module /{print $2; exit}' go.mod)"

VERSION_FILES=("VERSION" "internal/buildinfo/VERSION")
for f in "${VERSION_FILES[@]}"; do
  if [ ! -f "$f" ]; then
    echo "error: expected version file $f not found" >&2
    exit 1
  fi
done

if [ ! -f CHANGELOG.md ]; then
  echo "error: expected CHANGELOG.md not found — the release script writes the new version's section into it" >&2
  exit 1
fi

# ── 2b. The next release is declared, not computed ──────────────────
# NEXT_VERSION names the release main is building toward. It is not always
# VERSION's patch+1 (v0.1.46 was followed by v1.0.0), and the lint rollout
# rows, codemods and migration playbooks on main are keyed to it — their tests
# fail a key that is neither shipped nor NEXT_VERSION — so cutting any other
# version would ship them under a release they do not name. Omit the version
# to cut NEXT_VERSION; a pre-release of it (vX.Y.Z-rc.1) is allowed. To cut a
# different release, change NEXT_VERSION in a pull request first. A final
# release moves NEXT_VERSION to its next patch in the release commit.
NEXT_FILE="NEXT_VERSION"
if [ ! -f "$NEXT_FILE" ]; then
  echo "error: expected $NEXT_FILE not found — it names the release to cut" >&2
  exit 1
fi
NEXT="$(tr -d '[:space:]' <"$NEXT_FILE")"
if ! echo "$NEXT" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "error: $NEXT_FILE must hold one vX.Y.Z release (got: $NEXT)" >&2
  exit 1
fi
if [ -z "$VERSION" ]; then
  VERSION="$NEXT"
  echo "→ releasing $VERSION (from $NEXT_FILE)"
fi
if [ "${VERSION%%-*}" != "$NEXT" ]; then
  echo "error: $VERSION is not the next release: $NEXT_FILE names $NEXT." >&2
  echo "hint: omit the version to cut $NEXT, or change $NEXT_FILE in a pull request first." >&2
  exit 1
fi
RELEASE_TAG="$VERSION"
NEXT_AFTER="$NEXT"
case "$VERSION" in
  *-*) ;; # a pre-release: the final release is still next
  *) IFS=. read -r next_major next_minor next_patch <<<"${NEXT#v}"
     NEXT_AFTER="v$next_major.$next_minor.$((next_patch + 1))" ;;
esac

# ── 3. Clean tree ───────────────────────────────────────────────────
# The release worktree step 2a just made, which nothing else writes: a dirty
# one means something is wrong with this run, not with the caller's checkout
# (whose uncommitted work the release never sees).
if [ -n "$(git status --porcelain)" ]; then
  echo "error: working tree is not clean; commit or set aside your changes before releasing" >&2
  git status --short >&2
  exit 1
fi
START_HEAD="$(git rev-parse HEAD)"

# ── 4. The tag may not already exist ────────────────────────────────
if git rev-parse -q --verify "refs/tags/$RELEASE_TAG" >/dev/null; then
  echo "error: tag $RELEASE_TAG already exists (versions are immutable; bump instead)" >&2
  exit 1
fi

# ── 5. The version must not already be PUBLISHED at another commit ──
# proxy.golang.org is IMMUTABLE. Once it has served a version, that content is
# permanent: deleting the tag and re-cutting it at a different commit does
# NOT change what the proxy serves, and there is no way to correct it. The
# version is burned forever.
#
# This really happened. pkg/v0.1.12 was tagged, published, deleted, and
# re-cut elsewhere; the proxy still serves the first tree, which does not
# compile. The symptom is maddening from inside the repo — `git show <tag>`
# plainly contains your code while every consumer gets the old bytes — so the
# gate belongs HERE, before a tag exists, not in the debugging afterwards.
#
# Step 4 only sees LOCAL tags, and the poisoning case is precisely the one
# where the local tag was deleted. Only the proxy knows.
#
# A network failure must NOT pass this check. "The proxy said 404" and "curl
# could not reach the proxy" are the same empty output and must never be
# conflated, so curl's exit status is inspected before its body: unreachable
# is a hard error, and --skip-proxy-check is the only way past it.
#
# THE QUESTION IS ASKED OF THE PROXY'S CACHE, NOT OF THE PROXY.
# proxy.golang.org caches a 404 for a version nobody has requested yet for up
# to 30 minutes ("if someone requested the version before the tag was pushed, it
# may take up to 30 minutes for the mirror's cache to expire"). This check runs
# BEFORE the tag is pushed, so asking for <version>.info planted exactly that
# negative entry: v1.0.4 was pushed at 17:48Z and `go install ...@v1.0.4` failed
# for everyone while /@v/list already named it. Every release was uninstallable
# for half an hour, by our own hand.
#
# So the .info is requested through /cached-only (proxy.golang.org: "return only
# cached content"): the proxy answers from what it already holds and never
# fetches, so the request plants nothing. And what it holds is exactly the
# question: a version the proxy has ever served is in its cache, immutable, even
# after its tag was deleted upstream — the deleted-and-recut case this gate
# exists for. @v/list is NOT a substitute: it can be stale and need not name a
# version whose tag is gone while its .info is still served, so a version absent
# from it is not proof of anything.
proxy_published_commit() {
  # Echoes the published commit hash, or nothing if the version is unpublished.
  # Exits 1 (with a diagnosis on stderr) if the proxy could not be consulted.
  local module="$1" version="$2" body http_code curl_status hash url
  url="$PROXY_BASE/cached-only/$module/@v/$version.info"
  # -sS keeps the progress meter off but lets curl's own diagnosis reach
  # stderr, which is worth more than the exit code alone when this fails.
  body="$(curl -sS -w '\n%{http_code}' --max-time 30 "$url")" && curl_status=0 || curl_status=$?

  if [ "$curl_status" -ne 0 ]; then
    echo "error: could not reach the Go module proxy to check whether $version is already published" >&2
    echo "       (curl exited $curl_status for $url)" >&2
    echo "hint: this check is NOT optional — publishing over an existing version is" >&2
    echo "      unrecoverable, so a proxy it cannot read is a hard stop. Fix the" >&2
    echo "      network, or pass --skip-proxy-check if you have verified BY HAND" >&2
    echo "      that $version has never been published." >&2
    return 1
  fi

  http_code="$(printf '%s' "$body" | tail -n 1)"
  body="$(printf '%s' "$body" | sed '$d')"

  case "$http_code" in
    404|410)
      # The proxy has never served this version: it is free. (Asked of the
      # cache, so this answer did not plant a 404 for the version.)
      return 0 ;;
    200)
      ;;
    *)
      echo "error: the Go module proxy returned HTTP $http_code for $url" >&2
      echo "       $body" >&2
      echo "hint: this check cannot be answered, and publishing over an existing" >&2
      echo "      version is unrecoverable. Retry, or pass --skip-proxy-check if" >&2
      echo "      you have verified BY HAND that $version has never been published." >&2
      return 1 ;;
  esac

  # Published. Pull Origin.Hash out of the .info JSON without a jq dependency.
  hash="$(printf '%s' "$body" | grep -o '"Hash":"[0-9a-fA-F]*"' | head -n 1 | cut -d'"' -f4)"
  if [ -z "$hash" ]; then
    # Published but the origin commit is unknown, so equality cannot be
    # proven. Refusing is the only safe reading: an unprovable match is a
    # possible overwrite of immutable bytes.
    echo "error: $module@$version is ALREADY PUBLISHED on the module proxy, and the" >&2
    echo "       response carries no origin commit to compare against:" >&2
    echo "       $body" >&2
    echo "hint: proxy.golang.org is immutable — this version can never be corrected." >&2
    echo "      Bump to the next version." >&2
    return 1
  fi
  printf '%s\n' "$hash"
}

if [ "$SKIP_PROXY_CHECK" = "1" ]; then
  echo "→ SKIPPING the immutable-version proxy check (--skip-proxy-check)"
  echo "  you are asserting that $VERSION has NEVER been published. If it has,"
  echo "  this release is permanently broken and cannot be fixed."
else
  echo "→ checking $VERSION is not already published on the module proxy"
  # The commit that will carry the tag: HEAD of the release worktree, the
  # commit step 2a chose. An already-published version can only legitimately
  # match it (a re-run after a successful push); every other match means the
  # tag is about to move, which is the poisoning case.
  TAG_COMMIT="$(git rev-parse HEAD)"
  module="$ROOT_MODULE"
  if ! PUBLISHED_COMMIT="$(proxy_published_commit "$module" "$VERSION")"; then
    exit 1
  fi
  if [ -n "$PUBLISHED_COMMIT" ] && [ "$PUBLISHED_COMMIT" = "$TAG_COMMIT" ]; then
    echo "  $module@$VERSION: already published at $PUBLISHED_COMMIT (this commit) — idempotent"
  elif [ -z "$PUBLISHED_COMMIT" ]; then
    echo "  $module@$VERSION: not published — free to use"
  else
    echo "error: REFUSING TO RELEASE — $VERSION IS ALREADY PUBLISHED AT A DIFFERENT COMMIT." >&2
    echo "" >&2
    echo "  module:            $module" >&2
    echo "  published commit:  $PUBLISHED_COMMIT   (what proxy.golang.org serves today)" >&2
    echo "  commit to be used: $TAG_COMMIT   (what you are about to tag)" >&2
    echo "" >&2
    echo "This is NOT a network problem and NOT a transient error. The proxy answered" >&2
    echo "successfully: $module@$VERSION already exists, built from a different commit." >&2
    echo "" >&2
    echo "proxy.golang.org is IMMUTABLE. The content it has already served for" >&2
    echo "$VERSION is permanent. Deleting the tag and re-cutting it here will NOT" >&2
    echo "change what consumers download — they will keep getting $PUBLISHED_COMMIT" >&2
    echo "forever, and $VERSION can never be corrected by any action you take." >&2
    echo "" >&2
    echo "Release a NEW version instead. Treat $VERSION as burned and skip it." >&2
    exit 1
  fi
fi

# ── 6. Full tests at the exact source commit must be green ──────────
# A fast premerge green is not a release proof. Local fixture remotes have
# no GitHub CI; published GitHub releases fail closed, including dry runs.
# Step 2a already searched with this check; it is asked again here so a
# --commit the caller named is held to the same proof.
if [ -n "$RELEASE_REPO" ]; then
  bash "$RELEASE_SCRIPT_DIR/check-release-tests.sh" "$RELEASE_REPO" "$(git rev-parse HEAD)"
else
  echo "→ local/non-GitHub remote: no hosted full-test proof (not a GitHub publication)"
fi

# ── 7. Edit the release files ───────────────────────────────────────
# Everything below mutates the release worktree, which is removed on exit.
# The files are still backed up and restored on ANY exit that is not a
# completed real release, so the edits never outlive a failed or dry run even
# if the worktree cannot be removed.
#
# Two locks are taken BEFORE anything is backed up or edited, with the cleanup
# that drops them registered first:
#   - the checkout lock (in the caller's git dir, step 2a): one run of this
#     script per checkout at a time, dry runs included. Runs from one checkout
#     share its refs/tags/, where step 9 creates the local tag.
#   - the release lock (release-lock.sh, real runs only): one release of this
#     repository at a time, across machines and pipelines; the release workflow
#     coalesces into a live one.
# Until the backups exist there is nothing to restore: RESTORE_ON_EXIT stays 0,
# so a run refused a lock never touches a file.
BACKUP_DIR="$(mktemp -d)"
RESTORE_ON_EXIT=0
CHANGELOG_FILE="CHANGELOG.md"
NPM_MANIFEST="npm/package.json"
TOUCHED=("${VERSION_FILES[@]}" "$NEXT_FILE" "$CHANGELOG_FILE")
[ -f "$NPM_MANIFEST" ] && TOUCHED+=("$NPM_MANIFEST")

# The checkout lock is flock(1) on a file in this worktree's git dir: the kernel
# arbitrates it, and drops it when its holder dies, however it dies. There is no
# fallback: a pid-file or mkdir lock must decide when a dead holder's lock may be
# taken over, and two runs that judge the same dead holder can both take it —
# exactly the concurrent edit this lock exists to prevent. So no flock, no
# release (Linux has it in util-linux; macOS: `brew install flock`).
checkout_lock_acquire() {
  if ! command -v flock >/dev/null 2>&1; then
    echo "error: flock(1) is not installed, and this script will not edit a shared checkout without it" >&2
    echo "hint: install it (Linux: util-linux; macOS: brew install flock), then re-run" >&2
    return 1
  fi
  # fd 9 is held for the rest of the run; the lock goes with the process.
  exec 9>>"$CHECKOUT_LOCK"
  flock -n 9 && return 0
  echo "error: another release-forge.sh run is editing this checkout ($CHECKOUT_LOCK); wait for it to finish" >&2
  return 1
}
checkout_lock_release() {
  # Dropped before the EXIT trap ends, not left to the processes this run
  # started that still hold fd 9.
  flock -u 9 2>/dev/null || true
}

restore_tree() {
  if [ "$RESTORE_ON_EXIT" = "1" ]; then
    for f in "${TOUCHED[@]}"; do
      backup="$BACKUP_DIR/$(echo "$f" | tr '/' '_')"
      if [ -f "$backup" ]; then
        cp "$backup" "$f"
      else
        # The file did NOT exist before this run, so restoring means removing
        # it. A repo with no go.sum yet (or a fixture) would otherwise be left
        # with a stray untracked file that the next run's clean-tree gate
        # rejects — a dry run must leave NO trace.
        rm -f "$f"
      fi
    done
  fi
  rm -rf "$BACKUP_DIR" "${CLONE_DIR:-}"
  release_lock_release
  checkout_lock_release
  remove_release_worktree
}
trap restore_tree EXIT

checkout_lock_acquire || exit 1
# Re-judge under the lock what was judged before it: a run that held it until just
# now may have committed (HEAD moved) or tagged.
if [ -n "$(git status --porcelain)" ]; then
  echo "error: working tree is not clean; commit or set aside your changes before releasing" >&2
  git status --short >&2
  exit 1
fi
if [ "$(git rev-parse HEAD)" != "$START_HEAD" ]; then
  echo "error: HEAD moved while this release was validating ($START_HEAD -> $(git rev-parse HEAD)); re-run it" >&2
  exit 1
fi
if git rev-parse -q --verify "refs/tags/$RELEASE_TAG" >/dev/null; then
  echo "error: tag $RELEASE_TAG already exists (another cut made it)" >&2
  exit 1
fi
if [ "$DRY_RUN" != "1" ]; then
  # One release of this repository at a time (see above). Then re-check origin, since
  # a holder that just finished has released the lock and pushed the tag.
  release_lock_acquire "$VERSION" || exit 1
  if git ls-remote --exit-code --tags origin "refs/tags/$RELEASE_TAG" >/dev/null 2>&1; then
    echo "error: $RELEASE_TAG is already on origin (another cut released it)" >&2
    exit 1
  fi
fi

for f in "${TOUCHED[@]}"; do
  [ -f "$f" ] && cp "$f" "$BACKUP_DIR/$(echo "$f" | tr '/' '_')"
done
RESTORE_ON_EXIT=1

echo "→ syncing version files to $VERSION"
for f in "${VERSION_FILES[@]}"; do
  # internal/buildinfo/VERSION is a build-time copy of the root VERSION and
  # must stay BYTE-identical (TestEmbeddedVersionFileMatchesSource enforces
  # it) — an embed directive cannot reach outside its own package directory.
  printf '%s\n' "$VERSION" > "$f"
done
if [ "$NEXT_AFTER" != "$NEXT" ]; then
  echo "→ $NEXT_FILE: $NEXT → $NEXT_AFTER"
  printf '%s\n' "$NEXT_AFTER" > "$NEXT_FILE"
fi

# The npm shim's version IS the forge version it downloads. Only the manifest's
# own top-level "version" line is rewritten (two-space indent), never a
# dependency's.
if [ -f "$NPM_MANIFEST" ]; then
  echo "→ syncing $NPM_MANIFEST to ${VERSION#v}"
  sed -e 's/^  "version": ".*"/  "version": "'"${VERSION#v}"'"/' "$NPM_MANIFEST" >"$BACKUP_DIR/npm-package.json.new"
  if ! grep -q "^  \"version\": \"${VERSION#v}\"" "$BACKUP_DIR/npm-package.json.new"; then
    echo "error: could not set the version in $NPM_MANIFEST (expected a two-space-indented \"version\" line)" >&2
    exit 1
  fi
  cat "$BACKUP_DIR/npm-package.json.new" >"$NPM_MANIFEST"
fi

# ── 7b. CHANGELOG.md ────────────────────────────────────────────────
# Written only here, from the merged PR titles since the previous tag. The
# previous tag is the nearest v* tag reachable from HEAD (web-runtime/* tags do
# not match); with none, only the Unreleased prose is folded in.
GH_BIN="${FORGE_RELEASE_GH:-gh}"
PREV_TAG="$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD 2>/dev/null || true)"
RELEASE_DATE="$(date -u +%Y-%m-%d)"
GITHUB_REPO="$RELEASE_REPO"

# git is the source of truth (it works offline); gh only enriches it with the
# PRs labelled `breaking`, which a conventional-commit title cannot always say.
BREAKING_PRS=""
if [ -n "$PREV_TAG" ] && [ -n "$GITHUB_REPO" ] && command -v "$GH_BIN" >/dev/null 2>&1; then
  prev_date="$(git log -1 --format=%cs "$PREV_TAG")"
  if BREAKING_PRS="$("$GH_BIN" pr list --repo "$GITHUB_REPO" --state merged --label breaking \
      --search "merged:>=$prev_date" --limit 200 --json number --jq '.[].number' 2>/dev/null)"; then
    BREAKING_PRS="$(echo "$BREAKING_PRS" | tr '\n' ' ')"
  else
    BREAKING_PRS=""
    echo "  note: could not ask GitHub for \`breaking\` PRs; only commit titles mark breaking changes"
  fi
fi

echo "→ writing CHANGELOG.md: $VERSION from ${PREV_TAG:-<no previous tag>}"
NOTES_DIR="$BACKUP_DIR/notes"
mkdir -p "$NOTES_DIR"
"$(dirname "$0")/changelog-release.sh" \
  --changelog "$CHANGELOG_FILE" --version "$VERSION" --date "$RELEASE_DATE" \
  --prev-tag "$PREV_TAG" --breaking-prs "$BREAKING_PRS" \
  ${GITHUB_REPO:+--repo-url "https://github.com/$GITHUB_REPO"} \
  --generated-out "$NOTES_DIR/generated.md"

# The same prettier the pre-commit hook runs (pinned in .pre-commit-config.yaml,
# held equal to templates.PrettierVersion by TestPrettierVersionIsPinnedEverywhere).
# This is CHANGELOG.md's ONLY formatter: the pre-commit hooks exclude the file,
# because no pull request may edit it to fix what they would report. The whole
# file is formatted, so drift that reached main any other way is fixed here.
format_changelog() {
  if [ -n "${FORGE_RELEASE_PRETTIER:-}" ]; then
    $FORGE_RELEASE_PRETTIER "$CHANGELOG_FILE"
    return
  fi
  # The pin, not scripts/prettier.sh itself: that script drops CHANGELOG.md,
  # as the hook does, and this is the one place the file is formatted.
  local pin
  pin="$("$(dirname "$0")/prettier.sh" --version-pin)" || {
    echo "error: no prettier pin found in .pre-commit-config.yaml; cannot format CHANGELOG.md" >&2
    return 1
  }
  npx --yes "prettier@$pin" --write --ignore-unknown "$CHANGELOG_FILE" >/dev/null
}
echo "→ formatting CHANGELOG.md with prettier"
format_changelog

# The release notes are the new section, extracted back out of the formatted
# file. GitHub caps a release body at 125000 characters; a section that folds a
# long-lived Unreleased in can exceed it, and then the notes are the generated
# bullets alone with a pointer to the full section.
awk -v ver="$VERSION" '
  /^```/ { fence = !fence }
  !fence && index($0, "## [" ver "]") == 1 { on = 1; next }
  on && !fence && /^## / { exit }
  on { print }
' "$CHANGELOG_FILE" > "$NOTES_DIR/section.md"
NOTES_FILE="$NOTES_DIR/section.md"
if [ "$(wc -c < "$NOTES_FILE")" -gt 120000 ]; then
  {
    cat "$NOTES_DIR/generated.md"
    printf '\nThe full release section, including notes carried over from before this release, is in [CHANGELOG.md](https://github.com/%s/blob/%s/CHANGELOG.md).\n' "${GITHUB_REPO:-reliant-labs/forge}" "$VERSION"
  } > "$NOTES_DIR/short.md"
  NOTES_FILE="$NOTES_DIR/short.md"
  echo "  release notes exceed GitHub's limit; the GitHub release will carry the generated bullets only"
fi

# ── 8. The module still builds ──────────────────────────────────────
# There is no submodule require to resolve any more, and therefore no go.sum
# trap: forge is one module, so a consumer's `go mod download` needs hashes
# for forge's own DEPENDENCIES, which are already in go.sum from ordinary
# development. The elaborate local-bare-clone dance this script used to
# perform existed solely to record hashes for a forge/pkg tag that was not
# pushed yet.
echo "→ building the module"
go build ./...

# ── 8b. Release archives ────────────────────────────────────────────
# Built before the bookkeeping commit exists, so a build failure leaves no
# commit or tag behind. The stamped commit is the tagged one (HEAD here); the
# bookkeeping commit changes only VERSION, NEXT_VERSION, npm/package.json and
# CHANGELOG.md, so the compiled source is the tagged source.
ASSETS_DIR="$BACKUP_DIR/assets"
echo "→ building release archives"
FORGE_RELEASE_COMMIT="$(git rev-parse HEAD)" "$RELEASE_SCRIPT_DIR/build-release-assets.sh" "$VERSION" "$ASSETS_DIR"
ASSET_FILES=("$ASSETS_DIR"/forge_*.tar.gz "$ASSETS_DIR/checksums.txt")

# ── 9. Commit, tag, push (or describe the plan) ─────────────────────
if [ "$DRY_RUN" = "1" ]; then
  echo ""
  echo "DRY RUN: all validations passed."
  echo "DRY RUN: would stage: ${TOUCHED[*]}"
  echo "DRY RUN: would commit: chore: release $VERSION"
  echo "DRY RUN: CHANGELOG.md section that would be written (first 40 lines):"
  head -n 40 "$NOTES_DIR/section.md" | sed 's/^/DRY RUN:   /'
  if [ -n "$GITHUB_REPO" ]; then
    echo "DRY RUN: would run: gh release create $RELEASE_TAG --repo $GITHUB_REPO --notes-file <that section>"
  else
    echo "DRY RUN: origin is not on github.com; would skip the GitHub release"
  fi
  echo "DRY RUN: would attach: $(for f in "${ASSET_FILES[@]}"; do basename "$f"; done | tr '\n' ' ')"
  echo "DRY RUN: would tag $RELEASE_TAG at $COMMIT (already on origin/$BRANCH)"
  echo "DRY RUN: would push:   git push --atomic origin refs/tags/$RELEASE_TAG <bookkeeping commit>:refs/heads/release/$VERSION"
  echo "DRY RUN: would open:   a pull request release/$VERSION -> $BRANCH, set to auto-merge (nothing is pushed to $BRANCH)"
  echo ""
  echo "DRY RUN: removing the release worktree; no commit, tag, push or pull request was created."
  exit 0
fi

# Stage ONLY the files this script edited, by explicit path.
git add -- "${TOUCHED[@]}"
git commit -q -m "chore: release $VERSION

Sync VERSION and internal/buildinfo/VERSION; name $NEXT_AFTER in NEXT_VERSION;
write the $VERSION section of CHANGELOG.md from the PR titles merged since ${PREV_TAG:-the start}.

Bookkeeping for $RELEASE_TAG, tagged at $COMMIT."

# The tag names the green commit (step 2a), which is already on origin/$BRANCH;
# the bookkeeping commit on top of it reaches $BRANCH only as a pull request.
RELEASE_SHA="$COMMIT"
BOOKKEEPING_BRANCH="release/$VERSION"
git tag -a "$RELEASE_TAG" -m "forge $VERSION" "$COMMIT"
echo ""
echo "✅ committed the bookkeeping as $(git rev-parse --short HEAD) and tagged $RELEASE_TAG at $(git rev-parse --short "$COMMIT")"
echo ""
echo "→ pushing atomically (tag + $BOOKKEEPING_BRANCH, all-or-nothing; nothing goes to $BRANCH)"
# --atomic keeps the tag from landing without its bookkeeping branch, so a
# re-run never finds half a release.
#
# A refused push deletes the local tag this run just made (step 4 proved it did
# not exist before). Otherwise a cutter that lost a race leaves its own tag
# object under refs/tags/, and every git worktree of a checkout shares that
# namespace. On the release machine a losing v1.0.5 cut left a second v1.0.5 tag
# in the shared forge checkout, so every later `git fetch --tags` there failed
# with "would clobber existing tag" and the next release could not start (forge#1524).
# Publish only while this release provably still holds the lock (release-lock.sh).
if ! release_lock_assert; then
  git tag -d "$RELEASE_TAG" >/dev/null
  exit 1
fi
if ! git push --atomic origin "refs/tags/$RELEASE_TAG" "HEAD:refs/heads/$BOOKKEEPING_BRANCH"; then
  git tag -d "$RELEASE_TAG" >/dev/null
  echo "error: origin refused the push (git's reason is above), so nothing was released; deleted the local tag $RELEASE_TAG" >&2
  # Say which refusal it was. Only an origin that already has the tag means
  # another cut won; anything else is about this push, and reading every
  # refusal as a lost race sends the cutter to fetch a tag that is not there.
  if [ -n "$(git ls-remote --tags origin "refs/tags/$RELEASE_TAG" 2>/dev/null)" ]; then
    echo "       origin already has $RELEASE_TAG: another cut released it. Fetch it; do not re-cut." >&2
  elif [ -n "$(git ls-remote --heads origin "refs/heads/$BOOKKEEPING_BRANCH" 2>/dev/null)" ]; then
    echo "       origin has no $RELEASE_TAG, but already has a branch $BOOKKEEPING_BRANCH (an earlier attempt" >&2
    echo "       at this release?). Delete it if nothing uses it (git push origin :refs/heads/$BOOKKEEPING_BRANCH), then re-run." >&2
  else
    echo "       origin has no $RELEASE_TAG, so no other cut won a race: the push itself was refused" >&2
    echo "       (a ruleset or branch protection, missing permission, or the network). Fix that, then re-run." >&2
  fi
  exit 1
fi
# The bookkeeping commit is pushed; the worktree holding it can go on exit.
RESTORE_ON_EXIT=0

if [ -z "$GITHUB_REPO" ]; then
  echo "→ origin is not on github.com; merge $BOOKKEEPING_BRANCH into $BRANCH by hand"
elif ! { "$GH_BIN" pr create --repo "$GITHUB_REPO" --base "$BRANCH" --head "$BOOKKEEPING_BRANCH" \
      --title "chore: release $VERSION" --body "Bookkeeping for $RELEASE_TAG, tagged at $COMMIT." &&
    "$GH_BIN" pr merge --repo "$GITHUB_REPO" --squash --auto "$BOOKKEEPING_BRANCH"; }; then
  echo "⚠️  open the $BOOKKEEPING_BRANCH bookkeeping PR into $BRANCH by hand; $RELEASE_TAG is pushed" >&2
fi

echo ""
echo "✅ tagged and pushed $VERSION at $RELEASE_SHA"

# ── 9b. The GitHub release ──────────────────────────────────────────
# The tag is pushed and immutable by now, so nothing here can un-release it. A
# failure is reported loudly (and fails the script at the end) with the exact
# command to retry, but it must not stop the proxy wait below.
GH_RELEASE_FAILED=0
if [ -z "$GITHUB_REPO" ]; then
  echo "→ origin is not on github.com; skipping the GitHub release"
elif ! command -v "$GH_BIN" >/dev/null 2>&1; then
  GH_RELEASE_FAILED=1
  echo "⚠️  gh is not installed; create the GitHub release by hand:" >&2
  echo "    gh release create $RELEASE_TAG --repo $GITHUB_REPO --title $RELEASE_TAG --notes-file <section of CHANGELOG.md> <archives from: scripts/build-release-assets.sh $VERSION dist/>" >&2
else
  PRERELEASE_FLAG=()
  case "$VERSION" in *-*) PRERELEASE_FLAG=(--prerelease) ;; esac
  echo "→ creating the GitHub release $RELEASE_TAG"
  if ! "$GH_BIN" release create "$RELEASE_TAG" --repo "$GITHUB_REPO" --verify-tag \
      --title "$RELEASE_TAG" --notes-file "$NOTES_FILE" "${PRERELEASE_FLAG[@]}" "${ASSET_FILES[@]}"; then
    GH_RELEASE_FAILED=1
    echo "⚠️  $RELEASE_TAG is tagged and pushed, but the GitHub release was not created. Retry with:" >&2
    echo "    gh release create $RELEASE_TAG --repo $GITHUB_REPO --verify-tag --title $RELEASE_TAG --notes-file <section of CHANGELOG.md> <archives from: scripts/build-release-assets.sh $VERSION dist/>" >&2
  fi
fi

# ── 9c. Homebrew ────────────────────────────────────────────────────
# The tap's update-forge-formula.yml regenerates Formula/forge.rb from this
# release's checksums.txt and opens a PR. Best effort: the release is already
# out, and the tap can be dispatched by hand (docs/releasing.md, "Installers").
case "$VERSION" in
  *-*) ;; # a prerelease is not offered through brew
  *)
    if [ "$GH_RELEASE_FAILED" = "0" ] && [ -n "$GITHUB_REPO" ] && command -v "$GH_BIN" >/dev/null 2>&1; then
      echo "→ asking the Homebrew tap to update Formula/forge.rb"
      if ! "$GH_BIN" api repos/reliant-labs/homebrew-reliant/dispatches \
          -f event_type=forge-release-published -f "client_payload[version]=${VERSION#v}" >/dev/null 2>&1; then
        echo "  note: could not dispatch the tap; run its 'Update forge formula' workflow with version ${VERSION#v}" >&2
      fi
    fi
    ;;
esac

# ── 9d. npm shim ────────────────────────────────────────────────────
# @reliantlabs/forge is published by .github/workflows/release-npm.yml, which
# the `release: published` event of the GitHub release above starts: npm
# trusted publishing (OIDC) only exists inside GitHub Actions, so no machine
# running this script holds an npm credential. A prerelease is never published.
case "$VERSION" in
  *-*) echo "→ $VERSION is a prerelease: the npm shim is not published" ;;
  *)
    if [ -f "$NPM_MANIFEST" ] && [ "$GH_RELEASE_FAILED" = "0" ] && [ -n "$GITHUB_REPO" ]; then
      echo "→ npm: release-npm.yml publishes @reliantlabs/forge ${VERSION#v} from the GitHub release (gh run list --repo $GITHUB_REPO --workflow release-npm.yml)"
    fi
    ;;
esac

# ── 10. Wait for the proxy to ingest the tag ────────────────────────
# The push above returns as soon as GitHub has the tag. proxy.golang.org has
# not heard of it yet — it discovers versions by polling — and sum.golang.org
# records a hash only after the proxy has fetched the tree. For a window
# measured in MINUTES, `go mod download $ROOT_MODULE@$VERSION` 404s for
# everyone, including our own CI.
#
# That window is why E2E Scaffold went red on every release commit for ~6
# minutes: it scaffolds a project requiring the version just tagged and runs
# `go mod tidy` against a proxy that does not have it. The release was always
# correct; the lane was just asking too early. A lane that is red on every
# release is a lane nobody believes, which is how a genuinely broken tag
# ships unnoticed.
#
# Step 5 used to make the window far longer than ingestion alone: it asked the
# proxy for <version>.info before the tag existed, and proxy.golang.org cached
# that 404 for up to 30 minutes. It now asks the proxy's cache (/cached-only),
# which never fetches and so plants no entry for the new version: what is left
# is ingestion itself (about a minute), and wait-for-go-proxy.sh's default
# window is sized for that.
#
# So the release does not report DONE until the version is actually
# resolvable. Everything downstream — the consumer bump in docs/releasing.md
# steps 2-3, and any CI triggered by the pushed commit — depends on it.
#
# A timeout here does NOT mean the release failed: the tag is pushed and
# immutable either way. The script says so explicitly, because the wrong
# reaction (delete the tag, re-cut it) burns the version permanently.
echo ""
# ONE PROXY, ONE OVERRIDE. The immutable-version check (step 1) and this wait
# ask the same proxy about the same version, so they read the same base:
# FORGE_RELEASE_PROXY_BASE, forwarded as the wait script's own seam unless the
# caller already set that. Two independent defaults meant a run pointed at a
# fixture proxy for step 1 still polled proxy.golang.org here — for a version
# that will never appear there — and sat out the full 600s window.
if GOPROXY_WAIT_BASE="${GOPROXY_WAIT_BASE:-$PROXY_BASE}" \
  "$(dirname "$0")/wait-for-go-proxy.sh" --module "$ROOT_MODULE" --version "$VERSION" --tag-pushed; then
  echo ""
  echo "✅ released $VERSION at $RELEASE_SHA"
  echo ""
  echo "Then propagate the bump to consumers — see docs/releasing.md steps 2-3."
  # The tag, the commit and the module are all out; only the GitHub release
  # page is missing, and the retry command was printed above.
  [ "$GH_RELEASE_FAILED" = "0" ] || exit 1
else
  echo ""
  echo "⚠️  $VERSION is TAGGED AND PUSHED — the release itself succeeded."
  echo "    The module proxy has not ingested it yet. Wait and re-check with:"
  echo "      scripts/wait-for-go-proxy.sh --module $ROOT_MODULE --version $VERSION --tag-pushed"
  echo "    Do NOT re-cut the tag. Hold off on the consumer bump until it resolves."
  exit 1
fi
