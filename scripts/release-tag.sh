#!/bin/bash

# Phase 2 of a release: tag the commit that ACTUALLY LANDED ON MAIN.
#
# Usage: ./scripts/release-tag.sh [vX.Y.Z] [--commit <sha>] [--dry-run]
# --commit selects a reviewed main ancestor explicitly and requires its EXACT
# complete green record; no metadata carry or gate waiver is allowed in this mode.
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
SKIP_RELEASE_GATE=false
REQUESTED_COMMIT=""

while [[ $# -gt 0 ]]; do
    arg="$1"
    shift
    case "$arg" in
        --commit)
            if [[ -n "$REQUESTED_COMMIT" || $# -eq 0 || ! "$1" =~ ^[0-9a-fA-F]{4,40}$ ]]; then
                echo "Error: --commit requires one unambiguous commit SHA (4-40 hex digits), specified once" >&2
                exit 1
            fi
            REQUESTED_COMMIT=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
            shift
            ;;
        --dry-run)
            DRY_RUN=true
            ;;
        --skip-release-gate)
            SKIP_RELEASE_GATE=true
            ;;
        v[0-9]*)
            REQUESTED_TAG="$arg"
            ;;
        -h|--help)
            echo "Usage: $0 [vX.Y.Z] [--commit <sha>] [--dry-run] [--skip-release-gate]"
            echo ""
            echo "  vX.Y.Z               Tag this specific version (default: read from origin/main)"
            echo "  --commit <sha>       Select this main ancestor; requires its exact full green record"
            echo "  --dry-run            Print exactly what would happen, create and push nothing"
            echo "  --skip-release-gate  Tag without a green release-gate record (a hotfix; say so in the notes)"
            exit 0
            ;;
        *)
            echo -e "${RED}Error: unrecognized argument '$arg'${NC}" >&2
            echo "Usage: $0 [vX.Y.Z] [--commit <sha>] [--dry-run] [--skip-release-gate]" >&2
            exit 1
            ;;
    esac
done

if [[ -n "$REQUESTED_COMMIT" && "$SKIP_RELEASE_GATE" == "true" ]]; then
    echo "Error: --commit cannot be combined with --skip-release-gate" >&2
    exit 1
fi

if ! git rev-parse --git-dir > /dev/null 2>&1; then
    echo -e "${RED}Error: Not in a git repository${NC}" >&2
    exit 1
fi

# One release of reliant at a time on this machine (release.sh takes the same
# lock): a second tagger fails here, naming who holds it. A dry run only
# reports a release in progress. Tagging is the release train's job
# (control-plane's scripts/release-train.sh); a chat labels its PR release:asap.
# shellcheck source=scripts/lib/release-lock.sh
. "$(dirname "$0")/lib/release-lock.sh"
if [[ "$DRY_RUN" == "true" ]]; then
    release_lock_status reliant || echo -e "${YELLOW}  A real run would refuse now (above); the dry run continues.${NC}" >&2
else
    release_lock reliant "release-tag.sh ${REQUESTED_TAG:-<version on main>}" || exit $?
fi

# Everything below reasons about origin/main, never the local checkout. A
# local `main` can be stale, ahead, or someone else's in-flight work; the tag
# has to describe what is actually published.
echo -e "${YELLOW}📡 Fetching origin...${NC}"
if [[ -n "$REQUESTED_COMMIT" ]]; then
    git fetch origin '+refs/heads/main:refs/remotes/origin/main' --tags --quiet
else
    git fetch origin main --tags --quiet
fi

if ! git rev-parse --verify --quiet origin/main >/dev/null; then
    echo -e "${RED}Error: origin/main does not exist${NC}" >&2
    exit 1
fi

# ── Which version are we tagging? ───────────────────────────────────────────
#
# Read from origin/main's electron/package.json, NOT from the working tree.
# The working tree can hold an unmerged bump, and tagging a version that main
# does not carry is precisely the failure this script exists to prevent.
MAIN_REF=origin/main
if [[ -n "$REQUESTED_COMMIT" ]]; then
    MAIN_REF=$(git rev-parse --verify 'refs/remotes/origin/main^{commit}')
fi
VERSION_ON_MAIN=$(git show "$MAIN_REF:electron/package.json" | node -p "JSON.parse(require('fs').readFileSync(0,'utf8')).version")

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
else
    TAG_QUERY_RC=$?
    if [[ -n "$REQUESTED_COMMIT" && "$TAG_QUERY_RC" != 2 ]]; then
        echo "Error: could not establish that $RELEASE_TAG is absent on origin" >&2
        exit 1
    fi
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
done < <(git rev-list --reverse "$MAIN_REF" -- electron/package.json)

if [[ -z "$RELEASE_COMMIT" ]]; then
    echo -e "${RED}Error: no commit on origin/main sets electron/package.json to ${NEW_VERSION}${NC}" >&2
    echo -e "${YELLOW}  Has the release PR merged? Run: gh pr list --state open --search 'release v${NEW_VERSION}'${NC}" >&2
    exit 1
fi

if [[ -n "$REQUESTED_COMMIT" ]]; then
    VERSION_COMMIT="$RELEASE_COMMIT"
    # Resolve object IDs, not revision expressions or a hex-looking ref name.
    # More than one matching object is ambiguous, even if one is not a commit.
    RELEASE_COMMIT=$(git rev-parse --disambiguate="$REQUESTED_COMMIT")
    if [[ ! "$RELEASE_COMMIT" =~ ^[0-9a-f]{40}$ ]] ||
        [[ "$(git cat-file -t "$RELEASE_COMMIT")" != commit ]]; then
        echo "Error: cannot resolve --commit '$REQUESTED_COMMIT' to an unambiguous commit" >&2
        exit 1
    fi
    if ! git merge-base --is-ancestor "$VERSION_COMMIT" "$RELEASE_COMMIT"; then
        echo "Error: explicit commit must include the version-introducing commit $VERSION_COMMIT" >&2
        exit 1
    fi
    if ! git show "$RELEASE_COMMIT:electron/package.json" | node -e '
        const j = JSON.parse(require("fs").readFileSync(0, "utf8"));
        process.exit(j.version === process.argv[1] ? 0 : 1);
    ' "$NEW_VERSION" || ! git show "$RELEASE_COMMIT:electron/package-lock.json" | node -e '
        const j = JSON.parse(require("fs").readFileSync(0, "utf8"));
        process.exit(j.version === process.argv[1] && j.packages?.[""]?.version === process.argv[1] ? 0 : 1);
    ' "$NEW_VERSION"; then
        echo "Error: explicit commit package and lock versions must all equal $NEW_VERSION" >&2
        exit 1
    fi
    # Explicit candidates always need canonical notes, including prereleases.
    if ! git show "$RELEASE_COMMIT:docs/data/releases/${RELEASE_TAG}.yaml" | node -e '
        const lines = require("fs").readFileSync(0, "utf8").split(/\r?\n/).filter(l => /^version:/.test(l));
        const v = lines.length === 1 && lines[0].slice(8).trim();
        process.exit([process.argv[1], `"${process.argv[1]}"`, `\x27${process.argv[1]}\x27`].includes(v) ? 0 : 1);
    ' "$RELEASE_TAG"; then
        echo "Error: canonical release notes at explicit commit must declare version $RELEASE_TAG" >&2
        exit 1
    fi
fi

# ── THE GUARD ───────────────────────────────────────────────────────────────
#
# The one check that would have caught this five releases ago. It is cheap,
# and it is the difference between a release that can be described by
# `git describe` and one that cannot.
if ! git merge-base --is-ancestor "$RELEASE_COMMIT" "$MAIN_REF"; then
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

# ── THE RELEASE GATE ────────────────────────────────────────────────────────
#
# A merge runs only what its diff can affect (scripts/merge-gate.sh), so main
# is judged in pieces. The other half of that bargain: the WHOLE suite must
# have passed on the commit a release is cut from (scripts/release-gate.sh:
# unscoped lint, the -race suite with every slow test, integration, e2e,
# generate and docs drift). A green on an ancestor carries when everything
# since is release metadata (this release PR's own version bump and notes).
# dev-docs/merge-and-release-gates.md.
if [[ "$SKIP_RELEASE_GATE" == "true" ]]; then
    echo -e "${YELLOW}⚠️  --skip-release-gate: tagging ${RELEASE_COMMIT:0:9} WITHOUT a green release-gate record. Say so in the release notes.${NC}" >&2
else
    echo -e "${YELLOW}🔎 Checking the release gate for ${RELEASE_COMMIT:0:9}...${NC}"
    set +e
    "$(dirname "$0")/release-gate.sh" check "$RELEASE_COMMIT"
    GATE_RC=$?
    set -e
    if [[ "$GATE_RC" == 0 && -n "$REQUESTED_COMMIT" ]]; then
        # check fetches the existing evidence, but may allow metadata carry.
        # show reads ONLY this commit. Require one complete record naming this
        # exact SHA, not even an identical tree or a green ancestor.
        if ! "$(dirname "$0")/release-gate.sh" show "$RELEASE_COMMIT" | jq -es --arg sha "$RELEASE_COMMIT" '
            ["npm","generate","docs","lint","build","webbuild","typecheck","vet","test","integration","e2e"] as $required |
            length == 1 and (.[0] |
                .sha == $sha and .result == "PASS" and
                (.steps | type == "array" and length == ($required | length)) and
                ([.steps[].name] | sort) == ($required | sort) and
                all(.steps[]; .result == "PASS"))
        ' >/dev/null; then
            echo "Error: --commit requires an exact eleven-step PASS record for $RELEASE_COMMIT; ancestor carry is not accepted" >&2
            GATE_RC=1
        fi
    fi
    if [[ "$GATE_RC" != 0 ]]; then
        echo -e "${RED}✖ REFUSING TO TAG: ${RELEASE_COMMIT:0:9} has no green release-gate record${NC}" >&2
        echo -e "${YELLOW}  Run the slow tier on it (40-70 min; it holds one heavy slot):${NC}" >&2
        echo -e "${BLUE}    ./scripts/release-gate.sh run ${RELEASE_COMMIT}${NC}" >&2
        if [[ -n "$REQUESTED_COMMIT" ]]; then
            echo "  then run this again. Explicit candidates cannot waive the release gate." >&2
        else
            echo -e "${YELLOW}  then run this again. A hotfix that cannot wait: --skip-release-gate.${NC}" >&2
        fi
        exit 1
    fi
fi

SUBJECT=$(git log -1 --format=%s "$RELEASE_COMMIT")
if [[ -n "$REQUESTED_COMMIT" ]]; then
    RELEASE_TREE=$(git rev-parse "$RELEASE_COMMIT^{tree}")
    # Refuse any observed main movement, even if its version is unchanged.
    # Never reselect the candidate to follow it. This runs in dry-run too.
    REMOTE_MAIN=$(git ls-remote --exit-code origin refs/heads/main)
    if [[ "$REMOTE_MAIN" != "$MAIN_REF"$'\trefs/heads/main' ]]; then
        echo "Error: origin/main moved during preflight; retry against the new trusted main" >&2
        exit 1
    fi
    echo "  Explicit candidate: $RELEASE_COMMIT"
    echo "  Tree: $RELEASE_TREE"
    echo "  Version source: $VERSION_COMMIT"
    echo "  Trusted main: $MAIN_REF"
fi

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
