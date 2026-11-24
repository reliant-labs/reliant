#!/usr/bin/env bash
# vitest-shard.sh — one leg of the merge queue's `vitest` job
# (.github/workflows/merge-queue.yml): vitest's shard i of n over the vitest
# files the merge gate's plan selects. The gate beside it
# (`merge-gate.sh --queue --skip-vitest`) runs everything else.
#
#   scripts/vitest-shard.sh --base <rev> --shard <i>/<n>
#
# Every leg plans the SAME change with the SAME rules, reach and FULL-fallback
# choice as the gate (tools/test-affected plan -reach "${MG_REACH:-direct}",
# run -web-full reached), so the n legs together run exactly the file list the
# gate would have run in one process (vitest --shard splits it), none twice and
# none dropped. A hub module (#2935 reached ~1100 files) no longer has one
# 4-CPU runner run them all inside the queue's 30 minutes.
#
# A plan that selects no vitest file is a pass that installs nothing, so the
# job always concludes success or failure, never skipped. Anything that stops
# the plan from being made or read fails the leg (fail closed): a leg never
# passes without having decided what to run.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

base="" shard=""
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base=${2:-}; shift 2 ;;
    --shard) shard=${2:-}; shift 2 ;;
    -h|--help) awk 'NR > 1 && /^set -euo pipefail/ { exit } NR > 1' "$0"; exit 0 ;;
    *) echo "vitest-shard: unknown argument $1" >&2; exit 2 ;;
  esac
done
if [ -z "$base" ] || ! [[ $shard =~ ^([1-9][0-9]*)/([1-9][0-9]*)$ ]] || ((BASH_REMATCH[1] > BASH_REMATCH[2])); then
  echo "usage: scripts/vitest-shard.sh --base <rev> --shard <i>/<n>" >&2
  exit 2
fi
command -v jq >/dev/null 2>&1 || { echo "vitest-shard: jq is not installed, so the plan cannot be read and no selected file could be run" >&2; exit 1; }

# The merge gate's reach (scripts/merge-gate.sh: reach=${MG_REACH:-direct}).
reach=${MG_REACH:-direct}
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
plan=$dir/plan.json
go run ./tools/test-affected plan -json -reach "$reach" -base "$base" >"$plan"
jq -e '.web | has("tests") and has("full")' "$plan" >/dev/null || { echo "vitest-shard: the plan has no web lane to read; refusing to guess what was selected" >&2; exit 1; }

# With -web-full reached a FULL fallback runs the reached files, as a scoped
# lane does: no file selected is nothing to run either way.
nweb=$(jq -r '.web.tests | length' "$plan")
if [ "$nweb" -eq 0 ]; then
  echo "vitest-shard $shard: the plan selects no vitest file; nothing to run"
  exit 0
fi
echo "vitest-shard $shard: shard $shard of the $nweb vitest file(s) the plan selects (web full: $(jq -r .web.full "$plan"))"

# The merge gate's frozen install (scripts/merge-gate.sh npm_install),
# skipped when node_modules is newer than the lockfile.
if ! [ frontends/web/node_modules/.modules.yaml -nt frontends/web/pnpm-lock.yaml ]; then
  pnpm install --dir frontends/web --frozen-lockfile --ignore-scripts --config.confirmModulesPurge=false --reporter=silent
fi

# -capture: recorded as lane web-affected of a run of forge's test record, as
# the merge gate records its own web step (scripts/test-capture.sh); the
# verdict is vitest's.
go run ./tools/test-affected run -plan "$plan" -base "$base" -lanes web -web-full reached -web-shard "$shard" -capture
