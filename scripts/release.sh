#!/bin/bash

# Script to create and publish releases to GitHub and Cloudflare R2
# Usage: ./scripts/release.sh [patch|minor|major|prerelease]
#
# This script will:
# 1. Update version in package.json
# 2. Create git tag
# 3. Push tag to GitHub
# 4. Trigger GitHub Actions to build and publish to R2
# 5. Make app available for download and auto-update

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
CHANGELOG_FILE="mintlify-docs/data/releases/${RELEASE_TAG}.yaml"
GENERATED_CHANGELOG="mintlify-docs/changelog.mdx"
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

# Check if we're on main branch (protected)
CURRENT_BRANCH=$(git branch --show-current)
if [[ $CURRENT_BRANCH == "main" ]]; then
    echo -e "${YELLOW}📋 Detected main branch - creating feature branch for release...${NC}"
    VERSION_BRANCH="release-$NEW_VERSION"
    git checkout -b $VERSION_BRANCH
    echo -e "${BLUE}Created branch: $VERSION_BRANCH${NC}"
fi

# Create git tag with v prefix
echo -e "${YELLOW}🏷️  Creating release commit and tag...${NC}"
git add electron/package.json
if [[ "$REQUIRES_CHANGELOG" == "true" ]]; then
    git add "$CHANGELOG_FILE" "$GENERATED_CHANGELOG"
fi
git commit -m "chore: release v$NEW_VERSION"
git tag "$RELEASE_TAG"

# Push changes and tags
echo -e "${YELLOW}🚀 Publishing release to GitHub...${NC}"
git push origin HEAD
git push origin "v$NEW_VERSION"

echo -e "${BLUE}📦 GitHub Actions will now build and publish to Cloudflare R2${NC}"
echo -e "${BLUE}Monitor progress: https://github.com/reliant-labs/reliant/actions${NC}"

# If we created a feature branch, create a PR
if [[ $CURRENT_BRANCH == "main" ]]; then
    echo -e "${YELLOW}📋 Creating pull request...${NC}"
    if command -v gh >/dev/null 2>&1; then
        gh pr create --title "chore: release v$NEW_VERSION" --body "Automated release of v$NEW_VERSION" --base main --head $VERSION_BRANCH
        echo -e "${GREEN}✅ Pull request created!${NC}"
    else
        echo -e "${YELLOW}GitHub CLI not found. Please create a PR manually:${NC}"
        echo -e "${BLUE}Branch: $VERSION_BRANCH${NC}"
        echo -e "${BLUE}Base: main${NC}"
        echo -e "${BLUE}Title: chore: release v$NEW_VERSION${NC}"
    fi
fi

echo -e "${GREEN}🎉 Release v$NEW_VERSION created and published successfully!${NC}"
echo -e "${BLUE}📥 Download URLs (available after build completes):${NC}"
echo -e "${BLUE}  Latest: https://downloads.reliantlabs.io/Reliant-latest-mac-arm64.dmg${NC}"
echo -e "${BLUE}  Versioned: https://downloads.reliantlabs.io/Reliant-${NEW_VERSION}-mac-arm64.dmg${NC}"
echo -e "${BLUE}Tag: ${RELEASE_TAG}${NC}"
echo -e "${BLUE}Commit: $(git rev-parse --short HEAD)${NC}"
if [[ "$REQUIRES_CHANGELOG" == "true" ]]; then
    echo -e "${BLUE}Changelog source: ${CHANGELOG_FILE}${NC}"
    echo -e "${BLUE}Generated docs page: ${GENERATED_CHANGELOG}${NC}"
else
    echo -e "${BLUE}Changelog: skipped for RC prerelease${NC}"
fi