#!/usr/bin/env bash
# rc-shape.sh — sourced by release-tag.sh and check-release-tags.sh: is a commit
# off main the head of an RC train set (control-plane docs/design/gitops-promotion.md)?
#
# A set's reliant commit R' is main plus the train's ONE pin commit, kept on origin as
# the head of release/vX.Y.Z-rc.N. A red set is fixed IN PLACE (owner, 2026-11-02): fix
# commits land on that branch, on top of the pin commit, and the head is re-gated. So the
# RC shape is: main, then the pin commit (not a merge, its parent on main; it carries an
# `RC-Set: vX.Y.Z-rc.N` trailer, or it is the head itself, as every set before fixes
# existed), then any fix commits. A side branch (a PR head) has no such pin commit.
#
# A fix may also be MERGED onto the RC branch (v1.32.1-rc.2, 2026-11-25: reliant#3568
# landed two fixes as merge commit 8264a23bf, and the set could not finalize). The pin
# commit is then the last commit off main on the head's FIRST-parent chain, and a line
# with merges is accepted only when every parent of every merge, and every other commit
# off main, descends from that pin commit: a merged branch was made from the RC branch.
# So a merge cannot bring in main commits the set was not cut from (a side based on, or
# that merged, main), commits of another branch, or a second cut: such a line carries
# exactly one RC-Set commit, merged sides included. A merge whose first parent is on main
# is never a pin commit. Linear lines are judged as before. control-plane's
# scripts/check-pin-ancestry.sh --rc (rc_shaped, cp#3641) holds a set's build to the
# same rule, so a head one accepts the other does too.

# rc_shaped <commit> <main ref>: 0 when <commit> has the RC shape over <main ref>. On
# failure the reason is in RC_SHAPE_WHY.
# shellcheck disable=SC2034 # RC_SHAPE_WHY is read by the scripts that source this file
rc_shaped() {
    local commit="$1" main="$2" base merges m p n_line n_desc n_set
    RC_SHAPE_WHY=""
    base=$(git rev-list --first-parent "$commit" "^$main" 2>/dev/null | tail -n 1)
    if [[ -z "$base" ]]; then
        RC_SHAPE_WHY="it has no commit off $main"
        return 1
    fi
    if git rev-parse -q --verify "${base}^2" >/dev/null 2>&1 \
        || ! git merge-base --is-ancestor "${base}^" "$main" 2>/dev/null; then
        RC_SHAPE_WHY="its first commit off $main, ${base:0:12}, is not a pin commit (a merge, or its parent is not on $main)"
        return 1
    fi
    [[ "$base" == "$(git rev-parse "$commit^{commit}")" ]] && return 0
    if ! git log -1 --format=%B "$base" | grep -Eq '^RC-Set: v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'; then
        RC_SHAPE_WHY="its first commit off $main, ${base:0:12}, carries no 'RC-Set: vX.Y.Z-rc.N' trailer"
        return 1
    fi
    merges=$(git rev-list --merges "$commit" "^$main" 2>/dev/null)
    [[ -z "$merges" ]] && return 0
    # Each merge's every parent descends from the pin commit: a parent on main, or
    # anything else not built on the cut, would bring in ungated commits.
    for m in $merges; do
        for p in $(git rev-list --parents -n 1 "$m" | cut -d' ' -f2-); do
            git merge-base --is-ancestor "$base" "$p" 2>/dev/null && continue
            if git merge-base --is-ancestor "$p" "$main" 2>/dev/null; then
                RC_SHAPE_WHY="merge ${m:0:12} has parent ${p:0:12}, a commit of $main: it brings in main commits the set was not cut from"
            else
                RC_SHAPE_WHY="merge ${m:0:12} has parent ${p:0:12}, which does not descend from the pin commit ${base:0:12} (a branch not made from the RC branch)"
            fi
            return 1
        done
    done
    # And every other commit off main descends from it (a merged side's own commits).
    n_line=$(git rev-list --count "$commit" "^$main")
    n_desc=$(git rev-list --count --ancestry-path "${base}..${commit}")
    if [[ "$((n_desc + 1))" != "$n_line" ]]; then
        RC_SHAPE_WHY="a commit on its line off $main does not descend from the pin commit ${base:0:12} (a merged branch not made from the RC branch)"
        return 1
    fi
    # Exactly one cut: the pin commit's RC-Set, and none on a merged side.
    n_set=$(git log --format=%B "$commit" "^$main" | grep -cE '^RC-Set: v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' || true)
    if [[ "$n_set" != 1 ]]; then
        RC_SHAPE_WHY="its line off $main carries $n_set 'RC-Set:' commits: it mixes cuts (a line with a merge may carry only the pin commit's)"
        return 1
    fi
}
