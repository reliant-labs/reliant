#!/bin/bash

# Audits every release tag against main, and fails on any NEW one that has
# drifted off the trunk.
#
# Usage: ./scripts/check-release-tags.sh [--tag vX.Y.Z] [--quiet]
#
#   (no args)      audit every vX.Y.Z tag in the repo
#   --tag vX.Y.Z   audit exactly one tag (what CI runs on a tag push)
#   --quiet        only print failures
#
# ── WHAT IT IS FOR ──────────────────────────────────────────────────────────
#
# release-tag.sh cannot be the only guard, because it only guards the tags IT
# creates. A tag made from the GitHub web UI, from a stale clone, or by hand at
# 2am bypasses it entirely — and the failure is silent: nothing about an
# off-main tag looks wrong until someone runs `git describe` months later and
# gets an answer five releases stale.
#
# So this runs in CI on the tag push, where it sees every tag however it was
# made, and it converts that silence into one red run somebody sees.
#
# ── THE FROZEN BASELINE, AND WHY IT IS A FLOOR AND NOT A LIST ───────────────
#
# The brief described this as five bad tags (v1.7.8..v1.7.12). Auditing the
# full set found TWENTY of 32 tags off main, going back to v1.2.3-rc1 —
# v1.3.0, v1.4.0, v1.4.1, v1.6.0-v1.6.3, v1.7.0 and more. The single-phase
# release flow has been stranding tags intermittently for months; the recent
# five are simply the ones that started costing something, because commit
# pinning made the newest tag load-bearing.
#
# That rules out an enumerated exemption list. A list of twenty is unreadable,
# nobody can tell which entries are deliberate, and every future incident
# arrives as a one-word edit that looks like maintenance.
#
# So the baseline is a VERSION FLOOR instead. Every tag at or below the floor
# is frozen history: published, referenced by Electron artifacts on R2 and by
# the Homebrew cask, and therefore never to be moved — moving a published tag
# is the same class of error as re-cutting a burned Go module version. Every
# tag ABOVE the floor is policed absolutely.
#
# The floor is the newest tag that existed when the two-phase flow landed. It
# encodes a date, not a judgement about any individual tag, which is what makes
# it safe to read at 2am: raising it is visibly an act of forgiveness for a
# real release, not a list entry that could plausibly be a typo fix.
#
# RAISING THIS IS A LAST RESORT AND ALWAYS WRONG BY DEFAULT. If a new tag lands
# off main, the fix is to cut the next version through the two-phase flow, not
# to move the floor past the mistake.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# The last release cut by the old single-phase flow. Tags after this one are
# required to be ancestors of main.
BASELINE_TAG="v1.7.12"

# version_le A B — true when A sorts at or below B under version ordering.
# `sort -V` handles the -rc suffixes consistently with the rest of this repo's
# tooling (it orders v1.7.12-rc1 before v1.7.12, which is what we want).
version_le() {
    [ "$1" = "$2" ] && return 0
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" = "$1" ]
}

SINGLE_TAG=""
QUIET=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tag)
            SINGLE_TAG="${2:-}"
            if [[ -z "$SINGLE_TAG" ]]; then
                echo -e "${RED}Error: --tag requires a value${NC}" >&2
                exit 1
            fi
            shift 2
            ;;
        --quiet)
            QUIET=true
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--tag vX.Y.Z] [--quiet]"
            exit 0
            ;;
        *)
            echo -e "${RED}Error: unrecognized argument '$1'${NC}" >&2
            exit 1
            ;;
    esac
done

say() { [[ "$QUIET" == "true" ]] || echo -e "$@"; }

if ! git rev-parse --git-dir > /dev/null 2>&1; then
    echo -e "${RED}Error: Not in a git repository${NC}" >&2
    exit 1
fi

# Prefer origin/main; fall back to a local main so the script is usable in a
# clone that has not fetched (and in CI, which checks out a detached tag).
if git rev-parse --verify --quiet origin/main >/dev/null; then
    MAIN_REF="origin/main"
elif git rev-parse --verify --quiet main >/dev/null; then
    MAIN_REF="main"
else
    echo -e "${RED}Error: neither origin/main nor main exists — cannot check ancestry.${NC}" >&2
    echo -e "${YELLOW}  In CI, check out with fetch-depth: 0 and fetch origin main.${NC}" >&2
    exit 1
fi

if [[ -n "$SINGLE_TAG" ]]; then
    TAGS="$SINGLE_TAG"
else
    TAGS=$(git tag --list 'v*' --sort=v:refname)
fi

if [[ -z "$TAGS" ]]; then
    say "${YELLOW}No release tags found — nothing to check.${NC}"
    exit 0
fi

say "${BLUE}Checking release tags against ${MAIN_REF}...${NC}"
say ""

FAILURES=""
GRANDFATHERED=0
ON_MAIN=0

for tag in $TAGS; do
    if ! git rev-parse --verify --quiet "refs/tags/$tag^{commit}" >/dev/null; then
        echo -e "${RED}Error: tag '$tag' does not exist${NC}" >&2
        exit 1
    fi
    commit=$(git rev-parse "refs/tags/$tag^{commit}")

    if git merge-base --is-ancestor "$commit" "$MAIN_REF" 2>/dev/null; then
        ON_MAIN=$((ON_MAIN + 1))
        say "  ${GREEN}✓${NC} $tag  ${commit:0:9}  on ${MAIN_REF}"
        continue
    fi

    # Off main. Is it frozen history (at or below the baseline)?
    if version_le "$tag" "$BASELINE_TAG"; then
        GRANDFATHERED=$((GRANDFATHERED + 1))
        say "  ${YELLOW}—${NC} $tag  ${commit:0:9}  off ${MAIN_REF} (pre-${BASELINE_TAG}, published — left alone deliberately)"
        continue
    fi

    FAILURES="$FAILURES $tag"
    say "  ${RED}✗${NC} $tag  ${commit:0:9}  ${RED}OFF ${MAIN_REF}${NC}"
done

say ""

if [[ -n "$FAILURES" ]]; then
    echo -e "${RED}✖ Release tags are not on ${MAIN_REF}:${NC}" >&2
    for tag in $FAILURES; do
        echo -e "${RED}    $tag${NC}" >&2
    done
    cat >&2 <<'EOF'

WHY THIS MATTERS

  A tag that is not an ancestor of main is invisible to everything that reads
  history from tags:

    * `git describe` on main reports a stale version.
    * Changelog-from-tags generates against the wrong range.
    * `go get` on main derives its pseudo-version from the highest tag
      REACHABLE from the commit, so pinning main produces a version that sorts
      BELOW the newest release — a content upgrade that is a version-number
      downgrade, which Go's minimum-version selection can silently undo.
    * "What is deployed?" stops being answerable by comparing a tag to main.

HOW THIS HAPPENS

  Tagging a `release-*` branch and then squash-merging it. The squash creates a
  new commit; the tag stays behind on the branch. That is what produced
  v1.7.8 through v1.7.12.

THE FIX

  Do NOT move the tag if it is already published — Electron artifacts and the
  Homebrew cask reference it by name. Cut the next version through the normal
  two-phase flow instead:

    ./scripts/release.sh <patch|minor|major|prerelease>   # opens the PR
    # ... merge the PR ...
    ./scripts/release-tag.sh                              # tags the merged commit

EOF
    exit 1
fi

say "${GREEN}✅ All release tags accounted for${NC}"
say "${BLUE}   ${ON_MAIN} on ${MAIN_REF}, ${GRANDFATHERED} pre-${BASELINE_TAG} historical (published, left alone)${NC}"
