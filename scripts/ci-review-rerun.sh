#!/usr/bin/env bash
# Re-runs a passed head's red `Reliant review` verdicts, one at a time, so a
# reviewed pull request is never held out of the merge queue by stale runs.
#
# Each pull request event runs the required check (.github/workflows/
# reliant-review.yml) in a check suite of its own, and GitHub holds a required
# check red while ANY suite's newest run of it is red or cancelled. The runs on
# `opened`/`labeled` before pr-review posts its marker are red by design; a
# branch cut before the check lost its concurrency group (reliant #2063, forge
# #1641) still runs that old definition, which cancels its own runs. A re-run
# reads the same exact-head marker and turns green.
#
#   ci-review-rerun.sh pr <number> <sha> [<run_id>]
#       With <run_id>, the caller is that green run of the check (its last
#       step), so the head is known to pass. Without it, the head must pass the
#       gate now. Either way a run IN PROGRESS is waited for first, and re-run
#       if it ends red: it may have read the gate before the marker existed,
#       after every green run had already looked. (A queued run reads the gate
#       when it starts, so it passes on its own.) A green run waits only for
#       runs created BEFORE it, so two green runs never wait on each other, and
#       a re-run (attempt > 1) heals nothing, so it waits on no one.
#   ci-review-rerun.sh sweep
#       every open pull request into main (reliant-review-rerun.yml's
#       schedule: heads whose runs use an older definition of the check).
#
# One re-run at a time per head: a run of the old definition shares the
# concurrency group reliant-review-<pr>, so two in flight cancel each other.
# A re-run that ends red again stops the heal: the head's verdict is red now
# (a cancelled one is retried, up to RERUN_MAX_ATTEMPTS per run).
# Merge-group runs are never touched. Exits 0 unless misused: healing is
# best-effort, and the verdict itself is the gate's.
set -euo pipefail
repo=${GITHUB_REPOSITORY:?}
workflow=${REVIEW_WORKFLOW:-reliant-review.yml}
gate=${REVIEW_GATE:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-review-marker.sh}
poll=${RERUN_POLL_SECONDS:-10}
wait_max=${RERUN_WAIT_SECONDS:-600}
max_attempts=${RERUN_MAX_ATTEMPTS:-4}
max_reruns=${RERUN_MAX:-20}
jobs=${RERUN_SWEEP_JOBS:-4}

runs() { # <sha>: this head's pull-request runs of the check, oldest first: id status conclusion attempt
  gh api --paginate "repos/$repo/actions/workflows/$workflow/runs?head_sha=$1&per_page=100" \
    --jq '.workflow_runs[] | select(.event != "merge_group") | [.created_at, .id, .status, (.conclusion // "-"), .run_attempt] | @tsv' |
    sort | cut -f2-
}

inflight() { # <calling run id> < runs: the first run in progress created before the caller: id attempt
  awk -F'\t' -v self="$1" '$1 == self { exit } $2 == "in_progress" { print $1 "\t" $4; exit }'
}

reds() { # <calling run id> < runs: the oldest completed red run but the caller: id conclusion attempt
  awk -F'\t' -v self="$1" '$1 != self && $2 == "completed" &&
    ($3 == "failure" || $3 == "timed_out" || $3 == "cancelled" || $3 == "startup_failure") { print $1 "\t" $3 "\t" $4; exit }'
}

wait_run() { # <id> <min attempt>: wait for that attempt to complete; prints its conclusion
  local deadline=$((SECONDS + wait_max)) st att concl
  while :; do
    IFS=$'\t' read -r st att concl < <(gh api "repos/$repo/actions/runs/$1" --jq '[.status, .run_attempt, (.conclusion // "-")] | @tsv')
    if [ "$st" = completed ] && [ "$att" -ge "$2" ]; then echo "$concl"; return 0; fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "::warning::run $1 attempt $2 did not complete in ${wait_max}s" >&2
      return 1
    fi
    sleep "$poll"
  done
}

heal() { # <pr> <sha> [<calling run id>]
  local n=$1 want=$2 self=${3:-} state head hrepo list busy pick id concl att cur skip=" " reruns=0
  IFS=$'\t' read -r state head hrepo < <(gh api "repos/$repo/pulls/$n" --jq '[.state, .head.sha, (.head.repo.full_name // "-")] | @tsv')
  [ "$state" = open ] || { echo "#$n is $state: nothing to heal"; return 0; }
  [ "$head" = "$want" ] || { echo "#$n: its head moved from $want to $head: nothing to heal on the old one"; return 0; }
  [ "$hrepo" = "$repo" ] || { echo "#$n: a fork's head never passes: nothing to heal"; return 0; }
  list=$(runs "$head")
  if [ -z "$(inflight "$self" <<<"$list")$(reds "$self" <<<"$list")" ]; then
    echo "#$n: no red verdict on $head"; return 0
  fi
  if [ -z "$self" ] && ! bash "$gate" pr "$n" > /dev/null 2>&1; then
    echo "#$n: pr-review has not passed $head: its red verdicts stand"; return 0
  fi
  while [ "$reruns" -lt "$max_reruns" ]; do
    list=$(runs "$head" | awk -F'\t' -v skip="$skip" 'index(skip, " " $1 " ") == 0')
    busy=$(inflight "$self" <<<"$list")
    if [ -n "$busy" ]; then
      IFS=$'\t' read -r id att <<<"$busy"
      echo "#$n: waiting for run $id, in progress"
      if ! concl=$(wait_run "$id" "$att"); then skip+="$id "; fi
      continue
    fi
    pick=$(reds "$self" <<<"$list")
    [ -n "$pick" ] || { echo "#$n: no red verdict left on $head"; return 0; }
    IFS=$'\t' read -r id concl att <<<"$pick"
    if [ "$att" -ge "$max_attempts" ]; then
      echo "::warning::#$n: run $id is $concl after $att attempts: leaving it"
      skip+="$id "; continue
    fi
    echo "#$n: re-running $concl run $id (attempt $att) on $head"
    reruns=$((reruns + 1))
    if ! gh run rerun "$id" --repo "$repo"; then
      # Another healer may have re-run it first: then wait for that attempt.
      cur=$(gh api "repos/$repo/actions/runs/$id" --jq .run_attempt)
      if [ "$cur" -le "$att" ]; then
        echo "::warning::#$n: could not re-run $id"
        skip+="$id "; continue
      fi
    fi
    if ! concl=$(wait_run "$id" $((att + 1))); then skip+="$id "; continue; fi
    case $concl in
      # Cancelled is not a verdict: another healer's re-run of an old
      # definition shares its concurrency group. Retry it (bounded above).
      cancelled) echo "#$n: run $id's re-run was cancelled: retrying"; continue ;;
      failure | timed_out | startup_failure)
        echo "#$n: run $id re-ran $concl: the head's verdict is red now, so the heal stops"
        return 0 ;;
    esac
    echo "#$n: run $id is now $concl"
  done
  echo "::warning::#$n: stopped after $max_reruns re-runs"
}

case ${1:-} in
  pr)
    [ $# -ge 3 ] || { echo "usage: $0 pr <number> <sha> [<run_id>]" >&2; exit 2; }
    heal "$2" "$3" "${4:-}" ;;
  sweep)
    gh api --paginate "repos/$repo/pulls?state=open&base=main&per_page=100" --jq '.[] | select(.draft | not) | [.number, .head.sha] | @tsv' |
      xargs -r -L1 -P "$jobs" bash "${BASH_SOURCE[0]}" pr ||
      echo "::warning::a heal failed; the next sweep retries it" ;;
  *) echo "usage: $0 pr <number> <sha> [<run_id>] | sweep" >&2; exit 2 ;;
esac
