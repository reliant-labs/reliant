#!/bin/bash

# Phase 2 of a release: tag the commit that ACTUALLY LANDED ON MAIN.
#
# Usage: ./scripts/release-tag.sh [vX.Y.Z] [--dry-run]
#
# Run this AFTER the release PR opened by ./scripts/release.sh has merged.
# With no version argument it reads the version from origin/main itself, so
# the common case is a bare `./scripts/release-tag.sh`.
#
# ── WHY THIS IS A SEPARATE COMMAND ──────────────────────────────────────────
#
# release.sh used to cut a `release-*` branch, bump the version there, tag THAT
# commit, and open a PR. main squash-merges (there is not one merge commit in
# its whole history), so the merged commit is a different object than the one
# that was tagged — and the tag stayed behind on the branch forever.
#
# That is not a hypothetical. Every tag from v1.7.8 through v1.7.12 is off
# main; `git describe --tags --abbrev=0 origin/main` still answers v1.7.7.
#
# The consequence that made it urgent: `go get` on main produces a
# pseudo-version derived from the highest tag REACHABLE from the pinned commit
# — v1.7.7 — so pinning main yields `v1.7.8-0.<date>-<sha>`, which sorts BELOW
# the released v1.7.12. Go's minimum-version selection can then silently undo a
# pin that is a content upgrade but a version-number downgrade.
#
# Tagging after the merge is the only option that is correct by construction
# here: tagging the merge commit needs merge commits (main has none), and
# committing straight to main gives up review of the changelog.
#
# ── WHY IT IS NOT A WORKFLOW ON MERGE ───────────────────────────────────────
#
# Because it would not work, and it would fail silently, which is worse than
# not existing. A tag pushed with the automatic GITHUB_TOKEN does NOT trigger
# `on: push: tags` workflows — GitHub suppresses that to prevent recursion. So
# an auto-tagging job would create the tag and neither `Release Electron App`
# nor `Build & Push Image` would ever run: a release with no artifacts, and no
# error anywhere. This repo has no PAT (its secrets are Apple/Azure/R2/GCP
# signing and publishing credentials plus a Homebrew-tap-scoped token), so the
# push has to carry a human credential. That is this script.

set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

DRY_RUN=false
REQUESTED_TAG=""

for arg in "$@"; do
    case "$arg" in
        --dry-run)
            DRY_RUN=true
            ;;
        v[0-9]*)
            REQUESTED_TAG="$arg"
            ;;
        -h|--help)
            echo "Usage: $0 [vX.Y.Z] [--dry-run]"
            echo ""
            echo "  vX.Y.Z     Tag this specific version (default: read from origin/main)"
            echo "  --dry-run  Print exactly what would happen, create and push nothing"
            exit 0
            ;;
        *)
            echo -e "${RED}Error: unrecognized argument '$arg'${NC}" >&2
            echo "Usage: $0 [vX.Y.Z] [--dry-run]" >&2
            exit 1
            ;;
    esac
done

if ! git rev-parse --git-dir > /dev/null 2>&1; then
    echo -e "${RED}Error: Not in a git repository${NC}" >&2
    exit 1
fi

# Everything below reasons about origin/main, never the local checkout. A
# local `main` can be stale, ahead, or someone else's in-flight work; the tag
# has to describe what is actually published.
echo -e "${YELLOW}📡 Fetching origin...${NC}"
git fetch origin main --tags --quiet

if ! git rev-parse --verify --quiet origin/main >/dev/null; then
    echo -e "${RED}Error: origin/main does not exist${NC}" >&2
    exit 1
fi

# ── Which version are we tagging? ───────────────────────────────────────────
#
# Read from origin/main's electron/package.json, NOT from the working tree.
# The working tree can hold an unmerged bump, and tagging a version that main
# does not carry is precisely the failure this script exists to prevent.
VERSION_ON_MAIN=$(git show origin/main:electron/package.json | node -p "JSON.parse(require('fs').readFileSync(0,'utf8')).version")

if [[ -z "$VERSION_ON_MAIN" ]]; then
    echo -e "${RED}Error: could not read a version from origin/main:electron/package.json${NC}" >&2
    exit 1
fi

if [[ -n "$REQUESTED_TAG" ]]; then
    RELEASE_TAG="$REQUESTED_TAG"
    NEW_VERSION="${RELEASE_TAG#v}"
    if [[ "$NEW_VERSION" != "$VERSION_ON_MAIN" ]]; then
        echo -e "${RED}Error: you asked to tag $RELEASE_TAG, but origin/main carries version $VERSION_ON_MAIN${NC}" >&2
        echo -e "${YELLOW}  The release PR for $RELEASE_TAG has not merged yet, or you meant v${VERSION_ON_MAIN}.${NC}" >&2
        echo -e "${YELLOW}  Tagging a version main does not carry is how a tag ends up describing nothing.${NC}" >&2
        exit 1
    fi
else
    NEW_VERSION="$VERSION_ON_MAIN"
    RELEASE_TAG="v$NEW_VERSION"
fi

echo -e "${BLUE}🏷️  Preparing to tag ${RELEASE_TAG}${NC}"

# ── Has this already been released? ─────────────────────────────────────────
#
# Checked against the REMOTE as well as locally. A tag that exists upstream is
# published — Electron artifacts and the Homebrew cask reference it — and
# moving it is the same class of error as re-cutting a burned Go module
# version. This script will never do that.
if git rev-parse --verify --quiet "refs/tags/$RELEASE_TAG" >/dev/null; then
    EXISTING=$(git rev-parse "refs/tags/$RELEASE_TAG^{commit}")
    echo -e "${RED}Error: tag $RELEASE_TAG already exists locally (at ${EXISTING:0:9})${NC}" >&2
    echo -e "${YELLOW}  Published tags are never moved. If this release needs another build, cut a new version.${NC}" >&2
    exit 1
fi

if git ls-remote --exit-code --tags origin "refs/tags/$RELEASE_TAG" >/dev/null 2>&1; then
    echo -e "${RED}Error: tag $RELEASE_TAG already exists on origin${NC}" >&2
    echo -e "${YELLOW}  Published tags are never moved. If this release needs another build, cut a new version.${NC}" >&2
    exit 1
fi

# ── Which commit gets the tag? ──────────────────────────────────────────────
#
# The commit on origin/main that INTRODUCED this version, not origin/main's
# tip. Main keeps moving while a release is in flight, and tagging the tip
# would silently sweep whatever merged after the bump into the release.
#
# Walk the commits that touched electron/package.json oldest-first and take
# the first one carrying this version — that is the bump itself.
echo -e "${YELLOW}🔎 Locating the commit on origin/main that set version ${NEW_VERSION}...${NC}"

RELEASE_COMMIT=""
while read -r candidate; do
    [[ -z "$candidate" ]] && continue
    candidate_version=$(git show "$candidate:electron/package.json" 2>/dev/null \
        | node -p "try{JSON.parse(require('fs').readFileSync(0,'utf8')).version}catch(e){''}" 2>/dev/null || echo "")
    if [[ "$candidate_version" == "$NEW_VERSION" ]]; then
        RELEASE_COMMIT="$candidate"
        break
    fi
done < <(git rev-list --reverse origin/main -- electron/package.json)

if [[ -z "$RELEASE_COMMIT" ]]; then
    echo -e "${RED}Error: no commit on origin/main sets electron/package.json to ${NEW_VERSION}${NC}" >&2
    echo -e "${YELLOW}  Has the release PR merged? Run: gh pr list --state open --search 'release v${NEW_VERSION}'${NC}" >&2
    exit 1
fi

# ── THE GUARD ───────────────────────────────────────────────────────────────
#
# The one check that would have caught this five releases ago. It is cheap,
# and it is the difference between a release that can be described by
# `git describe` and one that cannot.
if ! git merge-base --is-ancestor "$RELEASE_COMMIT" origin/main; then
    echo -e "${RED}✖ REFUSING TO TAG: ${RELEASE_COMMIT:0:9} is not an ancestor of origin/main${NC}" >&2
    echo -e "${YELLOW}  A tag off the trunk breaks git describe, changelog-from-tags, and Go pseudo-versions.${NC}" >&2
    exit 1
fi

# The changelog for a stable release must be on main too — the tag and the
# release notes describing it should be the same commit, not two states of the
# repo that happen to both exist.
if [[ ! "$NEW_VERSION" =~ -rc ]]; then
    if ! git cat-file -e "$RELEASE_COMMIT:docs/data/releases/${RELEASE_TAG}.yaml" 2>/dev/null; then
        echo -e "${RED}Error: docs/data/releases/${RELEASE_TAG}.yaml is not present at ${RELEASE_COMMIT:0:9}${NC}" >&2
        echo -e "${YELLOW}  The release notes did not land with the version bump.${NC}" >&2
        exit 1
    fi
fi

SUBJECT=$(git log -1 --format=%s "$RELEASE_COMMIT")

echo ""
echo -e "${GREEN}✅ Verified — this tag will land on main${NC}"
echo -e "${BLUE}  Tag:     ${RELEASE_TAG}${NC}"
echo -e "${BLUE}  Commit:  ${RELEASE_COMMIT:0:9}  ${SUBJECT}${NC}"
echo -e "${BLUE}  Ancestor of origin/main: yes${NC}"
echo ""

if [[ "$DRY_RUN" == "true" ]]; then
    echo -e "${YELLOW}🧪 DRY RUN — nothing was created or pushed.${NC}"
    echo -e "${BLUE}Would run:${NC}"
    echo -e "${BLUE}  git tag $RELEASE_TAG $RELEASE_COMMIT${NC}"
    echo -e "${BLUE}  git push origin $RELEASE_TAG${NC}"
    exit 0
fi

echo -e "${YELLOW}🏷️  Creating and pushing ${RELEASE_TAG}...${NC}"
git tag "$RELEASE_TAG" "$RELEASE_COMMIT"
git push origin "$RELEASE_TAG"

echo ""
echo -e "${GREEN}🎉 ${RELEASE_TAG} published on main at ${RELEASE_COMMIT:0:9}${NC}"
echo -e "${BLUE}📦 GitHub Actions is now building: Release Electron App + Build & Push Image${NC}"
echo -e "${BLUE}Monitor: https://github.com/reliant-labs/reliant/actions${NC}"
echo ""
echo -e "${BLUE}📥 Download URLs (available after the build completes):${NC}"
echo -e "${BLUE}  Latest:    https://downloads.reliantlabs.io/Reliant-latest-mac-arm64.dmg${NC}"
echo -e "${BLUE}  Versioned: https://downloads.reliantlabs.io/Reliant-${NEW_VERSION}-mac-arm64.dmg${NC}"
