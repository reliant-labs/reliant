#!/usr/bin/env bash
# ci-queue-budget.sh — the merge queue's five-minute budget, reported.
#
#   scripts/ci-queue-budget.sh            (in ci.yml's `CI gate` job, merge_group only)
#
# Owner rule (2026-11-01): merge-queue CI takes at most five minutes of wall
# time, runner queue included. This compares the run's wall time (created ->
# now) with BUDGET (default 300s), splits it into runner wait (the slowest
# job's created -> started) and run time, and when it is over says so with a
# ::warning and on ONE rolling `pipeline:slo` issue (at most one comment an
# hour). REPORT ONLY: it always exits 0. A slow runner is not the change's
# fault, and failing the group would cost every entry behind it a rebuild.
set -u
budget=${BUDGET:-300}
repo=${GITHUB_REPOSITORY:?} run=${GITHUB_RUN_ID:?}

report() {
  local now created c wall jobs wait title url issue last line
  now=$(date +%s)
  created=$(gh api "repos/$repo/actions/runs/$run" -q .created_at) || return 0
  c=$(date -d "$created" +%s) || return 0
  wall=$((now - c))
  # Longest runner wait of any job that started (this gate job included).
  jobs=$(gh api "repos/$repo/actions/runs/$run/jobs?per_page=100" -q \
    '[.jobs[] | select(.started_at != null) | ((.started_at|fromdate) - (.created_at|fromdate))] | max // 0') || jobs=0
  wait=${jobs%.*}
  line="merge-queue wall ${wall}s (longest runner wait ${wait}s; budget ${budget}s); ${GITHUB_REF:-}"
  echo "### $line" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"
  if [ "$wall" -le "$budget" ]; then echo "$line"; return 0; fi
  echo "::warning title=Merge queue over its ${budget}s budget::$line"
  title="Merge queue CI over its 5-minute budget"
  url="${GITHUB_SERVER_URL:-https://github.com}/$repo/actions/runs/$run"
  gh label create pipeline:slo --color B60205 --description "A pipeline SLO is breached" >/dev/null 2>&1
  issue=$(gh issue list --state open --label pipeline:slo --search "in:title \"$title\"" \
    --json number,updatedAt -q '.[0] // empty | "\(.number) \(.updatedAt)"' 2>/dev/null)
  if [ -z "$issue" ]; then
    gh issue create --label pipeline:slo --title "$title" --body "$(printf '%s\n\n%s\n\n%s' \
      "The merge queue's CI (ci.yml on merge_group) has a five-minute wall-time budget, runner queue included (owner, 2026-11-01). This run was over it:" \
      "- $line ([run]($url))" \
      "A large runner wait is a runner-capacity problem (the org's 20-job cap), not a test one: fewer jobs per entry, or self-hosted runners. Further breaches are added as comments, at most one an hour.")" >/dev/null
    return 0
  fi
  last=$(date -d "${issue#* }" +%s 2>/dev/null) || return 0
  if [ $((now - last)) -ge 3600 ]; then
    gh issue comment "${issue%% *}" --body "- $line ([run]($url))" >/dev/null
  fi
  return 0
}
report || true
exit 0
