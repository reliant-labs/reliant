#!/usr/bin/env bash
# ci-nightly-issue.sh — keep ONE rolling "Nightly CI is red" issue for
# nightly.yml's report job.
#
#   RESULTS='<toJSON(needs)>' RUN_URL=<run url> scripts/ci-nightly-issue.sh
#
# The issue is found by its label, `ci:nightly-red`, never by its title, and
# in any state: a red nightly after a green one reopens it rather than filing
# another. While it stays red, a comment is added only when the SET of red jobs
# changes. Each report carries a hidden `<!-- nightly-red: <jobs> -->` marker,
# and an unchanged set is the same breakage: twelve identical comments in
# twelve days (#948) is noise that teaches people to mute the issue. A green
# nightly closes it.
set -euo pipefail

title="Nightly CI is red"
label="ci:nightly-red"

# A cancelled job did not verify anything either: a job that runs past
# timeout-minutes is reported as cancelled, not failed.
red=$(jq -r 'to_entries | sort_by(.key)[] | select(.value.result == "failure" or .value.result == "cancelled") | "- `\(.key)`: \(.value.result)"' <<<"$RESULTS")
jobs=$(jq -r '[to_entries | sort_by(.key)[] | select(.value.result == "failure" or .value.result == "cancelled") | "\(.key)=\(.value.result)"] | join(",")' <<<"$RESULTS")
marker="<!-- nightly-red: $jobs -->"

gh label create "$label" --force --color B60205 \
  --description "The nightly run on main is red; opened and closed by nightly.yml" >/dev/null
# Newest first. Before the label existed the issue was found by title, so an
# open, unlabelled issue of that title (#948) is adopted rather than duplicated.
latest=$(gh issue list --label "$label" --state all --limit 1 --json number,state --jq '.[] | "\(.number) \(.state)"')
if [ -z "$latest" ]; then
  latest=$(gh issue list --state open --search "\"$title\" in:title" --json number,title,state \
    --jq "[.[] | select(.title == \"$title\")][0] | select(. != null) | \"\(.number) \(.state)\"")
  [ -z "$latest" ] || gh issue edit "${latest%% *}" --add-label "$label" >/dev/null
fi
number="${latest%% *}"
state="${latest#* }"

if [ -z "$red" ]; then
  if [ -n "$number" ] && [ "$state" = OPEN ]; then
    gh issue close "$number" --comment "Green again: $RUN_URL"
    echo "closed #$number"
  else
    echo "nightly is green; no open issue"
  fi
  exit 0
fi

body=$(printf 'The nightly run on main is red: %s\n\n%s\n\nNightly checks are the ones moved off pull requests for speed (see `.github/workflows/nightly.yml`), so a pull request merged since the last green nightly is the likely cause.\n\n%s\n' "$RUN_URL" "$red" "$marker")
if [ -z "$number" ]; then
  gh issue create --title "$title" --label "$label" --body "$body"
elif [ "$state" != OPEN ]; then
  gh issue reopen "$number" >/dev/null
  gh issue comment "$number" --body "$body"
  echo "reopened #$number"
else
  # The last marker the issue carries (body, then comments, oldest first).
  last=$(gh issue view "$number" --json body,comments --jq '[.body, .comments[].body] | join("\n")' |
    sed -n 's/.*<!-- nightly-red: \(.*\) -->.*/\1/p' | tail -n 1)
  if [ "$last" = "$jobs" ]; then
    echo "#$number already reports these red jobs ($jobs); not commenting again"
  else
    gh issue comment "$number" --body "$body"
    echo "commented on #$number"
  fi
fi
echo "::error::nightly is red:"
echo "$red"
