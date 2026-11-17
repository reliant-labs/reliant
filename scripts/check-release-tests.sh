#!/usr/bin/env bash
# Fail closed before publishing: only the newest full CI run for this exact
# source commit counts. A PR/merge-queue green did not run the full tests.
set -euo pipefail
[[ $# == 2 ]] || { echo "usage: $0 OWNER/REPO SHA" >&2; exit 2; }
repo=$1 sha=$2
gh=${FORGE_RELEASE_GH:-gh}
refusal() {
  echo "error: release blocked: full tests for $sha are not proven green ($*)" >&2
  echo "hint: wait for main's CI, or dispatch ci.yml at this commit; every shard of Test (unit I/N) and Test (internal/cli I/N), and Test (-short whole tier, then guards), must all succeed." >&2
  exit 1
}
command -v "$gh" >/dev/null 2>&1 || refusal "gh is required"
# gh's jq filter is bundled: no extra jq dependency for release contributors.
run=$("$gh" run list --repo "$repo" --workflow ci.yml --commit "$sha" --limit 100 \
  --json databaseId,event,status,conclusion,headSha \
  --jq '[.[] | select(.event == "push" or .event == "workflow_dispatch")] | sort_by(.databaseId) | last | if . == null then "missing" else [.databaseId,.status,.conclusion,.headSha] | map(tostring) | join("|") end') || refusal "cannot read CI runs"
# "|", not tabs: a run still in progress has an EMPTY conclusion, and read
# collapses adjacent whitespace delimiters, shifting the sha into it.
IFS='|' read -r id status conclusion head <<<"$run"
# The run must have finished (not cancelled); its verdict is the three jobs
# below, so a red lint or Windows job elsewhere in the run does not block it.
# The run may still be finishing an unrelated job (CI gate, the aggregator,
# waiting for a runner); the three jobs below are the verdict either way. A
# cancelled run is refused: its jobs may have been cut short.
[[ "$conclusion" != cancelled && "$head" == "$sha" ]] || refusal "$run"
# CI gate may waive known main failures; the actual full test jobs cannot be
# skipped, cancelled, failed, or waived for a release, even if the gate is green.
#
# The full lanes are sharded matrices named `Test (unit I/N)` and
# `Test (internal/cli I/N)`. Every shard 1..N of each lane must be present and
# green: a shard that dropped out of the matrix is a slice of the suite that
# ran nowhere, so a lane is judged by the N its own names declare, and each
# lane needs at least one shard.
verdict=$("$gh" run view "$id" --repo "$repo" --json jobs \
  --jq '
    def lane($p): [.jobs[] | select(.name | test("^Test \\(" + $p + " [0-9]+/[0-9]+\\)$"))];
    def complete($js):
      [$js[] | .name | capture(" (?<i>[0-9]+)/(?<n>[0-9]+)\\)$") | {i: (.i | tonumber), n: (.n | tonumber)}] as $s
      | (($s | length) > 0)
        and (($s | map(.n) | unique | length) == 1)
        and (($s | map(.i) | sort) == [range(1; $s[0].n + 1)]);
    lane("unit") as $unit | lane("internal/cli") as $cli
    | [.jobs[] | select(.name == "Test (-short whole tier, then guards)")] as $guards
    | ($unit + $cli + $guards) as $all
    | if complete($unit) and complete($cli) and ($guards | length) == 1
         and all($all[]; .status == "completed" and .conclusion == "success")
      then "success" else "failure" end') || refusal "cannot read full test jobs"
[[ "$verdict" == success ]] || refusal "full test jobs missing or not successful"
echo "Full release tests passed at $sha (CI run $id)."
