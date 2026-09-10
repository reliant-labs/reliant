#!/bin/bash

# PHASE 1 of a release: propose the version bump. Creates NO tag.
# Usage: ./scripts/release.sh [patch|minor|major|prerelease]
#
# This script will:
# 1. Validate and regenerate the changelog
# 2. Update the version in electron/package.json (+ lockfile)
# 3. Open a PR against main
#
# Then, once that PR has MERGED:
#
#   ./scripts/release-tag.sh        # phase 2 — tags the merged commit on main
#
# Phase 2 is what creates the tag and triggers `Release Electron App` and
# `Build & Push Image`.
#
# ── WHY THE TAG MOVED OUT OF THIS SCRIPT ────────────────────────────────────
#
# This script used to cut a `release-*` branch, bump the version there, tag
# THAT commit, and open a PR. main squash-merges — there is not a single merge
# commit in its history — so the commit that landed was a different object than
# the one that got tagged, and the tag stayed behind on the branch forever.
#
# Every tag from v1.7.8 to v1.7.12 is off main because of this. To this day
# `git describe --tags --abbrev=0 origin/main` answers v1.7.7.
#
# Nothing warned. That is the part worth fixing: the tag was created, pushed,
# and built artifacts, so the release looked entirely successful. The damage
# only surfaces when something reads history from tags — `git describe`,
# changelog-from-tags, or `go get`, which derives a pseudo-version from the
# highest tag REACHABLE from the pinned commit. Pinning main therefore yields
# v1.7.8-0.<date>-<sha>, which sorts BELOW the released v1.7.12: a content
# upgrade that is a version-number downgrade, which Go's minimum-version
# selection can silently undo.
#
# The alternatives were considered and rejected. Tagging the merge commit
# requires merge commits, and main has none. Committing the bump straight to
# main removes the review step from the one change whose whole content is
# human-written release notes. Tagging after the merge is the only option that
# is correct BY CONSTRUCTION rather than by everyone remembering.

set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default to prerelease if no argument provided
RELEASE_TYPE=${1:-prerelease}

# Validate release type
case $RELEASE_TYPE in
    patch|minor|major|prerelease)
        ;;
    *)
        echo -e "${RED}Error: Invalid release type '$RELEASE_TYPE'${NC}"
        echo "Usage: $0 [patch|minor|major|prerelease]"
        echo "  patch: 0.2.3-rc1 → 0.2.3 (stable release from RC)"
        echo "  minor: 0.2.3 → 0.3.0 (new features)"
        echo "  major: 0.2.3 → 1.0.0 (breaking changes)"
        echo "  prerelease: 0.2.3 → 0.2.4-rc1 (first RC) or 0.2.4-rc1 → 0.2.4-rc2 (next RC)"
        exit 1
        ;;
esac

echo -e "${BLUE}🚀 Creating $RELEASE_TYPE release...${NC}"
echo -e "${YELLOW}Release type: $RELEASE_TYPE${NC}"

# Check if we're in a git repository
if ! git rev-parse --git-dir > /dev/null 2>&1; then
    echo -e "${RED}Error: Not in a git repository${NC}"
    exit 1
fi

# Check if there are uncommitted changes
if ! git diff-index --quiet HEAD --; then
    echo -e "${RED}Error: You have uncommitted changes. Please commit or stash them first.${NC}"
    exit 1
fi

# Get current version from package.json
CURRENT_VERSION=$(node -p "require('./electron/package.json').version")
echo -e "${YELLOW}Current version: $CURRENT_VERSION${NC}"

# Handle rc version increment specially
if [[ $RELEASE_TYPE == "prerelease" ]]; then
    # Support both -rc.N and -rcN formats
    if [[ $CURRENT_VERSION =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)-rc\.?([0-9]+)$ ]]; then
        # Already an RC version, increment RC number
        MAJOR=${BASH_REMATCH[1]}
        MINOR=${BASH_REMATCH[2]}
        PATCH=${BASH_REMATCH[3]}
        RC_NUMBER=${BASH_REMATCH[4]}
        NEW_RC_NUMBER=$((RC_NUMBER + 1))
        NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}-rc${NEW_RC_NUMBER}"
        echo -e "${YELLOW}📦 Creating release candidate: $CURRENT_VERSION → $NEW_VERSION${NC}"
    elif [[ $CURRENT_VERSION =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        # Stable version, create first RC of NEXT patch version
        MAJOR=${BASH_REMATCH[1]}
        MINOR=${BASH_REMATCH[2]}
        PATCH=${BASH_REMATCH[3]}
        NEW_PATCH=$((PATCH + 1))
        NEW_VERSION="${MAJOR}.${MINOR}.${NEW_PATCH}-rc1"
        echo -e "${YELLOW}📦 Creating first RC for next patch: $CURRENT_VERSION → $NEW_VERSION${NC}"
    else
        echo -e "${RED}Error: Cannot create RC from version $CURRENT_VERSION${NC}"
        exit 1
    fi

    # Update package.json directly
    cd electron
    node -e "const pkg = require('./package.json'); pkg.version = '$NEW_VERSION'; require('fs').writeFileSync('./package.json', JSON.stringify(pkg, null, 2) + '\n');"
    cd ..
else
    # Use npm version for other cases
    echo -e "${YELLOW}📦 Creating release: $CURRENT_VERSION → ${RELEASE_TYPE}${NC}"
    cd electron
    NEW_VERSION=$(npm version $RELEASE_TYPE --no-git-tag-version)
    cd ..
    # npm version returns with 'v' prefix, strip it
    NEW_VERSION=${NEW_VERSION#v}
fi

echo -e "${GREEN}✅ New version: $NEW_VERSION${NC}"

RELEASE_TAG="v$NEW_VERSION"
CHANGELOG_FILE="docs/data/releases/${RELEASE_TAG}.yaml"
GENERATED_CHANGELOG="docs/changelog.mdx"
REQUIRES_CHANGELOG=true

if [[ $RELEASE_TYPE == "prerelease" ]]; then
    REQUIRES_CHANGELOG=false
    echo -e "${YELLOW}ℹ️  Skipping changelog validation for RC prerelease ${RELEASE_TAG}${NC}"
fi

if [[ "$REQUIRES_CHANGELOG" == "true" ]]; then
    if [[ ! -f "$CHANGELOG_FILE" ]]; then
        echo -e "${RED}Error: Missing changelog source: $CHANGELOG_FILE${NC}"
        echo -e "${YELLOW}Create it before releasing:${NC}"
        echo -e "${BLUE}  make changelog${NC}"
        echo -e "${BLUE}  make changelog-draft VERSION=${RELEASE_TAG} [SINCE_TAG=vX.Y.Z]${NC}"
        echo -e "${BLUE}  edit ${CHANGELOG_FILE}${NC}"
        echo -e "${BLUE}  make generate-changelog${NC}"
        exit 1
    fi

    if ! command -v yq >/dev/null 2>&1; then
        echo -e "${RED}Error: yq is required to validate changelog YAML. Install with: brew install yq${NC}"
        exit 1
    fi

    VERSION_IN_CHANGELOG=$(yq -r '.version // ""' "$CHANGELOG_FILE")
    TITLE_IN_CHANGELOG=$(yq -r '.title // ""' "$CHANGELOG_FILE")
    SUMMARY_IN_CHANGELOG=$(yq -r '.summary // ""' "$CHANGELOG_FILE")
    ITEM_COUNT=$(yq -r '(.items // []) | length' "$CHANGELOG_FILE")

    if [[ "$VERSION_IN_CHANGELOG" != "$RELEASE_TAG" ]]; then
        echo -e "${RED}Error: $CHANGELOG_FILE version is '$VERSION_IN_CHANGELOG', expected '$RELEASE_TAG'${NC}"
        exit 1
    fi

    if [[ -z "$TITLE_IN_CHANGELOG" || "$TITLE_IN_CHANGELOG" == "null" ]]; then
        echo -e "${RED}Error: $CHANGELOG_FILE is missing a release title${NC}"
        exit 1
    fi

    if [[ -z "$SUMMARY_IN_CHANGELOG" || "$SUMMARY_IN_CHANGELOG" == "null" ]]; then
        echo -e "${RED}Error: $CHANGELOG_FILE is missing a release summary${NC}"
        exit 1
    fi

    if [[ "$ITEM_COUNT" == "0" ]]; then
        echo -e "${RED}Error: $CHANGELOG_FILE must contain at least one changelog item${NC}"
        exit 1
    fi

    echo -e "${YELLOW}📝 Regenerating Mintlify changelog from YAML...${NC}"
    make generate-changelog
fi

# Always cut a branch. Even when this is run from a non-main branch, the
# release commit belongs on its own branch so the PR contains the bump and
# nothing else.
CURRENT_BRANCH=$(git branch --show-current)
VERSION_BRANCH="release-$NEW_VERSION"
if [[ $CURRENT_BRANCH != "$VERSION_BRANCH" ]]; then
    echo -e "${YELLOW}📋 Creating release branch...${NC}"
    git checkout -b "$VERSION_BRANCH"
    echo -e "${BLUE}Created branch: $VERSION_BRANCH${NC}"
fi

echo -e "${YELLOW}📝 Creating release commit (no tag — see phase 2)...${NC}"
# `npm version` rewrites BOTH package.json and package-lock.json. Staging
# only the former left the lockfile's version field behind on every release
# — by v1.7.8 it still read 1.7.4, three releases stale — and left the
# working tree dirty after a release, which is exactly the state that hides
# a real uncommitted change.
git add electron/package.json electron/package-lock.json
if [[ "$REQUIRES_CHANGELOG" == "true" ]]; then
    git add "$CHANGELOG_FILE" "$GENERATED_CHANGELOG"
fi
git commit -m "chore: release v$NEW_VERSION"

# NO `git tag` HERE. The commit just created is not the commit that will land
# on main — main squash-merges, so the merged object is a different one.
# Tagging here is exactly what stranded v1.7.8..v1.7.12 off the trunk.

echo -e "${YELLOW}🚀 Pushing release branch...${NC}"
git push origin HEAD

echo -e "${YELLOW}📋 Creating pull request...${NC}"
PR_BODY="Version bump for v$NEW_VERSION.

**This PR does not create the tag.** After it merges, run:

\`\`\`
./scripts/release-tag.sh
\`\`\`

That tags the *merged* commit on \`main\` and triggers \`Release Electron App\`
and \`Build & Push Image\`. Tagging this branch instead would strand the tag off
the trunk once the PR is squash-merged — which is how v1.7.8 through v1.7.12
ended up unreachable from \`main\`."

if command -v gh >/dev/null 2>&1; then
    gh pr create --title "chore: release v$NEW_VERSION" --body "$PR_BODY" --base main --head "$VERSION_BRANCH"
    echo -e "${GREEN}✅ Pull request created!${NC}"
else
    echo -e "${YELLOW}GitHub CLI not found. Please create a PR manually:${NC}"
    echo -e "${BLUE}Branch: $VERSION_BRANCH${NC}"
    echo -e "${BLUE}Base: main${NC}"
    echo -e "${BLUE}Title: chore: release v$NEW_VERSION${NC}"
fi

echo ""
echo -e "${GREEN}🎉 Release v$NEW_VERSION proposed — phase 1 of 2 complete.${NC}"
echo -e "${BLUE}Branch: ${VERSION_BRANCH}${NC}"
echo -e "${BLUE}Commit: $(git rev-parse --short HEAD)${NC}"
if [[ "$REQUIRES_CHANGELOG" == "true" ]]; then
    echo -e "${BLUE}Changelog source: ${CHANGELOG_FILE}${NC}"
    echo -e "${BLUE}Generated docs page: ${GENERATED_CHANGELOG}${NC}"
else
    echo -e "${BLUE}Changelog: skipped for RC prerelease${NC}"
fi
echo ""
echo -e "${YELLOW}⚠️  NO TAG WAS CREATED, AND NOTHING IS BUILDING YET.${NC}"
echo -e "${YELLOW}   Next steps:${NC}"
echo -e "${BLUE}     1. Review and merge the PR above${NC}"
echo -e "${BLUE}     2. ./scripts/release-tag.sh${NC}"
echo ""
echo -e "${BLUE}   Step 2 tags the merged commit on main and starts the builds.${NC}"
echo -e "${BLUE}   Preview it any time with: ./scripts/release-tag.sh --dry-run${NC}"