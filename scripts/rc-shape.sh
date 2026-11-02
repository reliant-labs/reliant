#!/usr/bin/env bash
# rc-shape.sh — sourced by release-tag.sh and check-release-tags.sh: is a commit
# off main the head of an RC train set (control-plane docs/design/gitops-promotion.md)?
#
# A set's reliant commit R' is main plus the train's ONE pin commit, kept on origin as
# the head of release/vX.Y.Z-rc.N. A red set is fixed IN PLACE (owner, 2026-11-02): fix
# commits land on that branch, on top of the pin commit, and the head is re-gated. So the
# RC shape is: main, then the pin commit (its parent on main; it carries an
# `RC-Set: vX.Y.Z-rc.N` trailer, or it is the head itself, as every set before fixes
# existed), then any linear fix commits. A side branch (a PR head) has no such pin commit.

# rc_shaped <commit> <main ref>: 0 when <commit> has the RC shape over <main ref>.
rc_shaped() {
    local commit="$1" main="$2" base
    [[ -z "$(git rev-list --merges "$commit" "^$main" 2>/dev/null)" ]] || return 1
    base=$(git rev-list --reverse "$commit" "^$main" 2>/dev/null | head -1)
    [[ -n "$base" ]] || return 1
    git merge-base --is-ancestor "${base}^" "$main" 2>/dev/null || return 1
    [[ "$base" == "$(git rev-parse "$commit^{commit}")" ]] && return 0
    git log -1 --format=%B "$base" | grep -Eq '^RC-Set: v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'
}
