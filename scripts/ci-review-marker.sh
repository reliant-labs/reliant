#!/usr/bin/env bash
# Exact-head review gate (control-plane docs/pr-pipeline.md). The `Reliant review`
# workflow (.github/workflows/reliant-review.yml), the ruleset's required check,
# runs this from main on every pull request event and merge group.
# A pull request merges only if its CURRENT head carries pr-review's pass marker
# for that exact sha (`<!-- review-passed sha=<head> -->` in a review whose
# commit_id is the head), posted by the pipeline's reviewer. pr-disarm reacts to
# a push after the fact; this is checked by the merge itself, so a head pushed
# after a pass cannot ride an armed auto-merge in. A later blocker on the same
# sha wins.
#
#   ci-review-marker.sh pr <number>                 one pull request
#   ci-review-marker.sh group <head_ref> <head_sha> every pull request in a merge group
#
# Reviewers are GitHub user ids, not logins: reliant-labs[bot], the Reliant
# GitHub App (186876276), and, for now, the owner account reliant-01
# (225976313), which the pipeline acts as until the bot's issue writes work
# (reliant #1803, v1.8.9).
# TODO: drop 225976313 once the bot path is proven, matching pr-disarm/cp-merge.
set -euo pipefail
repo=${GITHUB_REPOSITORY:?}
reviewers=${REVIEW_MARKER_REVIEWERS:-186876276,225976313}

verdict() { # <pr> <sha>: the latest marker a reviewer posted for that sha, or none
  gh api --paginate "repos/$repo/pulls/$1/reviews?per_page=100" | jq -rs --arg sha "$2" --arg ids "$reviewers" '
    [.[][] | select(.commit_id == $sha and ((.user.id | tostring) as $i | $ids | split(",") | index($i)))
     | (.body // "") | capture("<!-- (?<v>review-(passed|changes)) sha=" + $sha + " -->")? | .v] | last // "none"'
}

case $1 in
  pr) prs=$2 ;;
  group)
    ours=$(sed -nE 's#.*/pr-([0-9]+)-[0-9a-f]+$#\1#p' <<<"$2")
    [ -n "$ours" ] || { echo "::error::cannot read a pull request number from $2"; exit 1; }
    # The group is main plus every entry at or ahead of ours (ALLGREEN: each also has its own run).
    entries=$(gh api graphql -F o="${repo%%/*}" -F r="${repo#*/}" -f query='query($o:String!,$r:String!){repository(owner:$o,name:$r){mergeQueue(branch:"main"){entries(first:100){nodes{position headCommit{oid} pullRequest{number}}}}}}' \
      --jq '.data.repository.mergeQueue.entries.nodes')
    prs=$(jq -r --arg sha "$3" --argjson n "$ours" '
      (map(select(.pullRequest.number == $n or .headCommit.oid == $sha)) | map(.position) | max // 0) as $p
      | [.[] | select(.position <= $p) | .pullRequest.number] + [$n] | unique | .[]' <<<"$entries") ;;
  *) echo "usage: $0 pr <number> | group <head_ref> <head_sha>" >&2; exit 2 ;;
esac

bad=0
for n in $prs; do
  head=$(gh api "repos/$repo/pulls/$n" --jq .head.sha)
  v=$(verdict "$n" "$head")
  if [ "$v" = review-passed ]; then echo "#$n: pr-review passed its head $head"
  else echo "::error::#$n: its head $head has no pr-review pass marker (latest: $v); pr-review must pass this exact head"; bad=1; fi
done
exit $bad
