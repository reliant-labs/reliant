#!/usr/bin/env bash
# Write this workflow run's per-job wall times to the step summary.
#
#   scripts/ci-run-timings.sh            (in a job of the run being reported)
#
# Needs GH_TOKEN with actions: read, GITHUB_REPOSITORY and GITHUB_RUN_ID —
# all present in a workflow job. ci.yml's `gate` job runs it last, after every
# other job has finished, so the table is the whole run.
#
# WHY. The pull-request path has a wall-time budget (about five minutes), and
# a budget nobody can see erodes one slow test at a time. This puts the number
# on every run: each job's wall time, how long it queued for a runner, and the
# critical path — run start to the last job's finish — so a regression shows
# up on the pull request that caused it rather than months later as "CI got
# slow".
set -euo pipefail

: "${GITHUB_REPOSITORY:?}" "${GITHUB_RUN_ID:?}"
summary=${GITHUB_STEP_SUMMARY:-/dev/stdout}

run=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID")
jobs=$(gh api --paginate "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/attempts/$(jq -r .run_attempt <<<"$run")/jobs?per_page=100" --jq '.jobs[]' | jq -s .)

# Jobs that ran: a skipped job has started_at == completed_at, and the job
# running this script has no completed_at yet.
jq -r --argjson run "$run" '
  def secs(a; b): ((b | fromdateiso8601) - (a | fromdateiso8601));
  def mmss(s): "\((s / 60) | floor)m\(((s % 60) | floor) | tostring | if length < 2 then "0" + . else . end)s";
  ($run.run_started_at) as $start
  | [.[] | select(.completed_at != null and .started_at != null and .conclusion != "skipped")] as $done
  | ($done | map(.completed_at | fromdateiso8601) | max // ($start | fromdateiso8601)) as $last
  | "### Job wall times",
    "",
    "Critical path (run start to last job finished): **\(mmss($last - ($start | fromdateiso8601)))**",
    "",
    "| job | result | wall | queued |",
    "| --- | --- | ---: | ---: |",
    ($done | sort_by(-secs(.started_at; .completed_at))[]
      | "| \(.name) | \(.conclusion) | \(mmss(secs(.started_at; .completed_at))) | \(mmss([secs(.created_at; .started_at), 0] | max)) |"),
    ""
' <<<"$jobs" >>"$summary"
